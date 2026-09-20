/** Project only combinations the runtime can resolve without changing the other option. */
export function profileOptions(profile, model, coordinatorRef) {
  const unavailable = {
    efforts: [],
    inheritEffort: false,
    fastOn: false,
    fastOff: false,
    inheritFast: false,
  };
  if (!model) return unavailable;
  const ref =
    !profile.model || profile.model === "inherit"
      ? coordinatorRef
      : profile.model;
  if (typeof ref !== "string" || ref.split("#")[0] !== model.ref)
    return unavailable;
  const variantID = ref.split("#")[1];
  const initial =
    model.variants.find((variant) => variant.id === variantID) ||
    (variantID === "default"
      ? model.variants.find((variant) => variant.id === undefined)
      : undefined);
  if (!initial) return unavailable;
  const effort =
    !profile.effort || profile.effort === "inherit"
      ? initial.effort
      : profile.effort;
  const fast =
    profile.fast === undefined || profile.fast === "inherit"
      ? initial.fast
      : profile.fast;
  const sameSpeed = model.variants.filter((variant) => variant.fast === fast);
  const sameEffort = model.variants.filter(
    (variant) => variant.effort === effort,
  );
  return {
    efforts: [
      ...new Set(
        sameSpeed
          .map((variant) => variant.effort)
          .filter((value) => typeof value === "string"),
      ),
    ],
    inheritEffort: sameSpeed.some(
      (variant) => variant.effort === initial.effort,
    ),
    fastOn: sameEffort.some((variant) => variant.fast === true),
    fastOff: sameEffort.some((variant) => variant.fast === false),
    inheritFast: sameEffort.some((variant) => variant.fast === initial.fast),
  };
}
