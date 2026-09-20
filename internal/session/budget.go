package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

const (
	legacyDailyBudgetSchemaVersion = 1
	dailyBudgetSchemaVersion       = 2
	maxDailyBudgetBytes            = 1 << 20
	dailyBudgetFileName            = "budget.ledger"
	maxDailyBudgetDays             = 2
	maxDailyBudgetReservations     = 128
	maxDailyBudgetSettlements      = 2048
	maxBudgetReservationIDBytes    = 128
	maxDailyBudgetReservationLease = time.Hour
	// Tombstones live at least as long as the longest reservation lease, so a
	// delayed retry cannot charge the same provider completion twice.
	dailyBudgetSettlementRetention = maxDailyBudgetReservationLease
)

// ErrDailyBudgetLimit is returned when a reservation would reach the daily
// cap. It is an admission refusal, not a persistence failure.
var ErrDailyBudgetLimit = errors.New("daily budget reservation reaches configured limit")

// DailyBudgetSnapshot is the authoritative state of one local-calendar day
// after a ledger transaction. ReservedUSD contains all other live provider
// liabilities as well as the caller's reservation, when still live.
type DailyBudgetSnapshot struct {
	Day            string
	SpentUSD       float64
	ReservedUSD    float64
	AlreadySettled bool
}

type dailyBudgetReservation struct {
	ID        string  `json:"id"`
	AmountUSD float64 `json:"amount_usd"`
	ExpiresAt string  `json:"expires_at"`
}

type dailyBudgetSettlement struct {
	ID        string  `json:"id"`
	ActualUSD float64 `json:"actual_usd"`
	ExpiresAt string  `json:"expires_at"`
}

type dailyBudgetDay struct {
	Date         string                   `json:"date"`
	SpentUSD     float64                  `json:"spent_usd"`
	Reservations []dailyBudgetReservation `json:"reservations,omitempty"`
	Settlements  []dailyBudgetSettlement  `json:"settlements,omitempty"`
}

type dailyBudgetRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Days          []dailyBudgetDay `json:"days"`
}

type legacyDailyBudgetRecord struct {
	SchemaVersion int     `json:"schema_version"`
	Date          string  `json:"date"`
	SpentUSD      float64 `json:"spent_usd"`
}

// UpdateDailyBudget atomically adds deltaUSD to today's local-calendar
// ledger and returns the authoritative actual spend. floorUSD is applied only
// when it exceeds the stored value, preserving the legacy configured seed
// without adding it on every process restart. Version-one ledgers are read
// without loss and migrate to version two on the next write.
func (s *Store) UpdateDailyBudget(ctx context.Context, project, day string, deltaUSD, floorUSD float64) (float64, error) {
	if invalidBudgetAmount(deltaUSD) || invalidBudgetAmount(floorUSD) {
		return 0, errors.New("daily budget: amounts must be finite and non-negative")
	}
	snapshot, err := s.mutateDailyBudget(ctx, project, day, false, func(recordDay *dailyBudgetDay, _ time.Time) (bool, bool, error) {
		changed := false
		if floorUSD > recordDay.SpentUSD {
			recordDay.SpentUSD = floorUSD
			changed = true
		}
		if deltaUSD > math.MaxFloat64-recordDay.SpentUSD {
			return false, false, errors.New("daily budget: total overflow")
		}
		recordDay.SpentUSD += deltaUSD
		return changed || deltaUSD > 0, false, nil
	})
	return snapshot.SpentUSD, err
}

// ReserveDailyBudget atomically admits or resizes one provider request. A
// reservation ID is unique to a provider turn. Reusing the ID replaces its
// amount, so retries cannot consume the estimate twice.
func (s *Store) ReserveDailyBudget(
	ctx context.Context,
	project, day, reservationID string,
	estimateUSD, maxDailyUSD, floorUSD float64,
	expiresAt time.Time,
) (DailyBudgetSnapshot, error) {
	if err := validateBudgetReservationID(reservationID); err != nil {
		return DailyBudgetSnapshot{}, err
	}
	if invalidBudgetAmount(estimateUSD) || invalidBudgetAmount(floorUSD) ||
		maxDailyUSD <= 0 || math.IsNaN(maxDailyUSD) || math.IsInf(maxDailyUSD, 0) {
		return DailyBudgetSnapshot{}, errors.New("daily budget: reservation amounts and cap must be finite and non-negative")
	}
	return s.mutateDailyBudget(ctx, project, day, false, func(recordDay *dailyBudgetDay, now time.Time) (bool, bool, error) {
		if expiresAt.IsZero() || !expiresAt.After(now) || expiresAt.After(now.Add(maxDailyBudgetReservationLease)) {
			return false, false, fmt.Errorf("daily budget: reservation lease must end within %s", maxDailyBudgetReservationLease)
		}
		for _, settlement := range recordDay.Settlements {
			if settlement.ID == reservationID {
				return false, false, errors.New("daily budget: reservation ID was already settled")
			}
		}

		changed := false
		if floorUSD > recordDay.SpentUSD {
			recordDay.SpentUSD = floorUSD
			changed = true
		}
		index := -1
		reservedUSD := 0.0
		for i, reservation := range recordDay.Reservations {
			if reservation.ID == reservationID {
				index = i
				continue
			}
			if reservation.AmountUSD > math.MaxFloat64-reservedUSD {
				return false, false, errors.New("daily budget: reservation total overflow")
			}
			reservedUSD += reservation.AmountUSD
		}
		if reservedUSD > math.MaxFloat64-recordDay.SpentUSD || estimateUSD > math.MaxFloat64-recordDay.SpentUSD-reservedUSD {
			return false, false, errors.New("daily budget: projected total overflow")
		}
		if projected := recordDay.SpentUSD + reservedUSD + estimateUSD; projected >= maxDailyUSD {
			return false, false, fmt.Errorf(
				"daily budget: estimated cost $%.4f reaches daily cap $%.4f (spent $%.4f, reserved $%.4f): %w",
				estimateUSD, maxDailyUSD, recordDay.SpentUSD, reservedUSD,
				ErrDailyBudgetLimit,
			)
		}

		reservation := dailyBudgetReservation{
			ID: reservationID, AmountUSD: estimateUSD, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
		}
		if index >= 0 {
			recordDay.Reservations[index] = reservation
		} else {
			if len(recordDay.Reservations) >= maxDailyBudgetReservations {
				return false, false, fmt.Errorf("daily budget: too many live reservations (max %d)", maxDailyBudgetReservations)
			}
			recordDay.Reservations = append(recordDay.Reservations, reservation)
		}
		_ = changed // floor changes are committed with the reservation write.
		return true, false, nil
	})
}

// SettleDailyBudget atomically replaces reservationID with the exact actual
// provider cost. A bounded settlement tombstone makes retries idempotent. A
// completion just after midnight is charged to its admission day, retained as
// the previous bucket, and never overwrites the new day's totals.
func (s *Store) SettleDailyBudget(ctx context.Context, project, day, reservationID string, actualUSD float64) (DailyBudgetSnapshot, error) {
	if err := validateBudgetReservationID(reservationID); err != nil {
		return DailyBudgetSnapshot{}, err
	}
	if invalidBudgetAmount(actualUSD) {
		return DailyBudgetSnapshot{}, errors.New("daily budget: actual cost must be finite and non-negative")
	}
	return s.mutateDailyBudget(ctx, project, day, true, func(recordDay *dailyBudgetDay, now time.Time) (bool, bool, error) {
		for _, settlement := range recordDay.Settlements {
			if settlement.ID != reservationID {
				continue
			}
			if settlement.ActualUSD != actualUSD {
				return false, false, errors.New("daily budget: settlement retry changed the actual cost")
			}
			return false, true, nil
		}
		if len(recordDay.Settlements) >= maxDailyBudgetSettlements {
			return false, false, fmt.Errorf("daily budget: too many recent settlements (max %d)", maxDailyBudgetSettlements)
		}
		removeDailyBudgetReservation(recordDay, reservationID)
		if actualUSD > math.MaxFloat64-recordDay.SpentUSD {
			return false, false, errors.New("daily budget: total overflow")
		}
		recordDay.SpentUSD += actualUSD
		recordDay.Settlements = append(recordDay.Settlements, dailyBudgetSettlement{
			ID: reservationID, ActualUSD: actualUSD,
			ExpiresAt: now.Add(dailyBudgetSettlementRetention).UTC().Format(time.RFC3339Nano),
		})
		return true, false, nil
	})
}

// ReleaseDailyBudget removes an estimate only when its request was certainly
// not dispatched. Missing and already-expired IDs are idempotent no-ops.
func (s *Store) ReleaseDailyBudget(ctx context.Context, project, day, reservationID string) (DailyBudgetSnapshot, error) {
	if err := validateBudgetReservationID(reservationID); err != nil {
		return DailyBudgetSnapshot{}, err
	}
	return s.mutateDailyBudget(ctx, project, day, true, func(recordDay *dailyBudgetDay, _ time.Time) (bool, bool, error) {
		return removeDailyBudgetReservation(recordDay, reservationID), false, nil
	})
}

func (s *Store) mutateDailyBudget(
	ctx context.Context,
	project, day string,
	allowPreviousDay bool,
	mutate func(recordDay *dailyBudgetDay, now time.Time) (changed, alreadySettled bool, err error),
) (DailyBudgetSnapshot, error) {
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return DailyBudgetSnapshot{}, fmt.Errorf("daily budget: invalid date %q", day)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var snapshot DailyBudgetSnapshot
	err := s.withRecordLock(ctx, project, "budget", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now()
		if s.now != nil {
			now = s.now()
		}
		today := now.Format(time.DateOnly)
		yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
		if day != today && (!allowPreviousDay || day != yesterday) {
			direction := "stale"
			if day > today {
				direction = "future"
			}
			return fmt.Errorf("daily budget: refusing %s update for %s (current day %s)", direction, day, today)
		}
		path := filepath.Join(s.dir, project, dailyBudgetFileName)
		record, missing, err := loadDailyBudgetRecord(path, now)
		if err != nil {
			return err
		}
		pruned := normalizeDailyBudgetRecord(&record, today, yesterday, now)
		recordDay := findDailyBudgetDay(&record, day)
		addedDay := false
		if recordDay == nil {
			if len(record.Days) >= maxDailyBudgetDays {
				return fmt.Errorf("daily budget: cannot retain more than %d calendar days", maxDailyBudgetDays)
			}
			record.Days = append(record.Days, dailyBudgetDay{Date: day})
			recordDay = &record.Days[len(record.Days)-1]
			addedDay = true
		}
		changed, alreadySettled, err := mutate(recordDay, now)
		if err != nil {
			return err
		}
		reservedUSD, err := dailyBudgetReservedTotal(*recordDay)
		if err != nil {
			return err
		}
		snapshot = DailyBudgetSnapshot{
			Day: day, SpentUSD: recordDay.SpentUSD, ReservedUSD: reservedUSD, AlreadySettled: alreadySettled,
		}
		writeNeeded := missing || pruned || changed
		if addedDay && !changed && !missing {
			// A release of an already-expired prior-day token is a read-only no-op.
			record.Days = record.Days[:len(record.Days)-1]
		}
		if !writeNeeded {
			return nil
		}
		record.SchemaVersion = dailyBudgetSchemaVersion
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("daily budget: encode ledger: %w", err)
		}
		if len(encoded) > maxDailyBudgetBytes {
			return errors.New("daily budget: encoded ledger exceeds size limit")
		}
		if err := writeFileAtomic(path, encoded, 0o600); err != nil {
			return fmt.Errorf("daily budget: write ledger: %w", err)
		}
		return nil
	})
	return snapshot, err
}

func loadDailyBudgetRecord(path string, now time.Time) (dailyBudgetRecord, bool, error) {
	data, err := readBoundedRegularFile(path, maxDailyBudgetBytes)
	if errors.Is(err, os.ErrNotExist) {
		return dailyBudgetRecord{SchemaVersion: dailyBudgetSchemaVersion}, true, nil
	}
	if err != nil {
		return dailyBudgetRecord{}, false, fmt.Errorf("daily budget: read ledger: %w", err)
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return dailyBudgetRecord{}, false, fmt.Errorf("daily budget: decode ledger: %w", err)
	}
	var record dailyBudgetRecord
	switch header.SchemaVersion {
	case legacyDailyBudgetSchemaVersion:
		var legacy legacyDailyBudgetRecord
		if err := json.Unmarshal(data, &legacy); err != nil {
			return dailyBudgetRecord{}, false, fmt.Errorf("daily budget: decode v1 ledger: %w", err)
		}
		record = dailyBudgetRecord{SchemaVersion: dailyBudgetSchemaVersion, Days: []dailyBudgetDay{{
			Date: legacy.Date, SpentUSD: legacy.SpentUSD,
		}}}
	case dailyBudgetSchemaVersion:
		if err := json.Unmarshal(data, &record); err != nil {
			return dailyBudgetRecord{}, false, fmt.Errorf("daily budget: decode ledger: %w", err)
		}
	default:
		return dailyBudgetRecord{}, false, fmt.Errorf("daily budget: unsupported schema version %d", header.SchemaVersion)
	}
	if err := validateDailyBudgetRecord(record, now); err != nil {
		return dailyBudgetRecord{}, false, err
	}
	return record, false, nil
}

func validateDailyBudgetRecord(record dailyBudgetRecord, now time.Time) error {
	if len(record.Days) > maxDailyBudgetDays {
		return fmt.Errorf("daily budget: ledger contains too many days (max %d)", maxDailyBudgetDays)
	}
	seenDates := make(map[string]struct{}, len(record.Days))
	seenIDs := make(map[string]struct{})
	today := now.Format(time.DateOnly)
	for _, recordDay := range record.Days {
		if _, err := time.Parse(time.DateOnly, recordDay.Date); err != nil {
			return fmt.Errorf("daily budget: ledger contains invalid date %q", recordDay.Date)
		}
		if recordDay.Date > today {
			return fmt.Errorf("daily budget: ledger date %s is in the future", recordDay.Date)
		}
		if _, exists := seenDates[recordDay.Date]; exists {
			return fmt.Errorf("daily budget: duplicate ledger day %s", recordDay.Date)
		}
		seenDates[recordDay.Date] = struct{}{}
		if invalidBudgetAmount(recordDay.SpentUSD) {
			return errors.New("daily budget: ledger contains an invalid total")
		}
		if len(recordDay.Reservations) > maxDailyBudgetReservations {
			return fmt.Errorf("daily budget: ledger contains too many reservations (max %d)", maxDailyBudgetReservations)
		}
		if len(recordDay.Settlements) > maxDailyBudgetSettlements {
			return fmt.Errorf("daily budget: ledger contains too many settlements (max %d)", maxDailyBudgetSettlements)
		}
		for _, reservation := range recordDay.Reservations {
			if err := validateBudgetReservationID(reservation.ID); err != nil {
				return fmt.Errorf("daily budget: invalid ledger reservation: %w", err)
			}
			if _, exists := seenIDs[reservation.ID]; exists {
				return fmt.Errorf("daily budget: duplicate reservation ID %q", reservation.ID)
			}
			seenIDs[reservation.ID] = struct{}{}
			if invalidBudgetAmount(reservation.AmountUSD) {
				return errors.New("daily budget: ledger contains an invalid reservation amount")
			}
			expiresAt, err := time.Parse(time.RFC3339Nano, reservation.ExpiresAt)
			if err != nil {
				return fmt.Errorf("daily budget: invalid reservation expiry: %w", err)
			}
			if expiresAt.After(now.Add(maxDailyBudgetReservationLease)) {
				return errors.New("daily budget: ledger contains an overlong reservation lease")
			}
		}
		for _, settlement := range recordDay.Settlements {
			if err := validateBudgetReservationID(settlement.ID); err != nil {
				return fmt.Errorf("daily budget: invalid ledger settlement: %w", err)
			}
			if _, exists := seenIDs[settlement.ID]; exists {
				return fmt.Errorf("daily budget: duplicate reservation ID %q", settlement.ID)
			}
			seenIDs[settlement.ID] = struct{}{}
			if invalidBudgetAmount(settlement.ActualUSD) {
				return errors.New("daily budget: ledger contains an invalid settlement amount")
			}
			expiresAt, err := time.Parse(time.RFC3339Nano, settlement.ExpiresAt)
			if err != nil {
				return fmt.Errorf("daily budget: invalid settlement expiry: %w", err)
			}
			if expiresAt.After(now.Add(dailyBudgetSettlementRetention)) {
				return errors.New("daily budget: ledger contains an overlong settlement retention")
			}
		}
	}
	return nil
}

func normalizeDailyBudgetRecord(record *dailyBudgetRecord, today, yesterday string, now time.Time) bool {
	changed := false
	keptDays := record.Days[:0]
	for i := range record.Days {
		recordDay := record.Days[i]
		liveReservations := recordDay.Reservations[:0]
		for _, reservation := range recordDay.Reservations {
			expiresAt, _ := time.Parse(time.RFC3339Nano, reservation.ExpiresAt)
			if !expiresAt.After(now) {
				changed = true
				continue
			}
			liveReservations = append(liveReservations, reservation)
		}
		recordDay.Reservations = liveReservations
		liveSettlements := recordDay.Settlements[:0]
		for _, settlement := range recordDay.Settlements {
			expiresAt, _ := time.Parse(time.RFC3339Nano, settlement.ExpiresAt)
			if !expiresAt.After(now) {
				changed = true
				continue
			}
			liveSettlements = append(liveSettlements, settlement)
		}
		recordDay.Settlements = liveSettlements
		if recordDay.Date != today && recordDay.Date != yesterday {
			changed = true
			continue
		}
		if recordDay.Date == yesterday && len(recordDay.Reservations) == 0 && len(recordDay.Settlements) == 0 {
			changed = true
			continue
		}
		keptDays = append(keptDays, recordDay)
	}
	record.Days = keptDays
	return changed
}

func findDailyBudgetDay(record *dailyBudgetRecord, day string) *dailyBudgetDay {
	for i := range record.Days {
		if record.Days[i].Date == day {
			return &record.Days[i]
		}
	}
	return nil
}

func dailyBudgetReservedTotal(recordDay dailyBudgetDay) (float64, error) {
	total := 0.0
	for _, reservation := range recordDay.Reservations {
		if reservation.AmountUSD > math.MaxFloat64-total {
			return 0, errors.New("daily budget: reservation total overflow")
		}
		total += reservation.AmountUSD
	}
	return total, nil
}

func removeDailyBudgetReservation(recordDay *dailyBudgetDay, reservationID string) bool {
	for i := range recordDay.Reservations {
		if recordDay.Reservations[i].ID != reservationID {
			continue
		}
		copy(recordDay.Reservations[i:], recordDay.Reservations[i+1:])
		recordDay.Reservations = recordDay.Reservations[:len(recordDay.Reservations)-1]
		return true
	}
	return false
}

func validateBudgetReservationID(id string) error {
	if id == "" || len(id) > maxBudgetReservationIDBytes {
		return fmt.Errorf("daily budget: reservation ID must contain 1-%d bytes", maxBudgetReservationIDBytes)
	}
	for _, r := range id {
		if r <= 0x20 || r == 0x7f {
			return errors.New("daily budget: reservation ID contains control or whitespace characters")
		}
	}
	return nil
}

func invalidBudgetAmount(amount float64) bool {
	return amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0)
}
