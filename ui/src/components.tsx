import React, {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { useKeyboard, usePaste } from "@opentui/react";
import type { ScrollBoxRenderable, TextareaRenderable } from "@opentui/core";
import { T, clean } from "./theme";

type Entry = {
  action?: () => void;
  input?: boolean;
  scroll?: () => ScrollBoxRenderable | null;
};
const Focus = createContext({
  id: "composer",
  set: (_id: string) => {},
  entries: new Map<string, Entry>(),
  modal: false,
});
export function FocusRoot({
  children,
  modal = null,
}: {
  children: React.ReactNode;
  modal?: string | null;
}) {
  const [id, set] = useState("composer");
  const entries = useRef(new Map<string, Entry>()).current;
  useEffect(() => {
    set(modal ? (entries.has("modal-search") ? "modal-search" : "modal-close") : "composer");
  }, [modal]);
  useEffect(() => {
    // Dialogs can overflow even on large terminals. Reveal the focused
    // control in its containing viewport after the layout has settled.
    const timer = setTimeout(() => {
      for (const entry of entries.values()) entry.scroll?.()?.scrollChildIntoView(id);
    }, 0);
    return () => clearTimeout(timer);
  }, [id, modal]);
  useKeyboard((key) => {
    const available = [...entries.keys()].filter((k) =>
      modal ? k.startsWith("modal-") : !k.startsWith("modal-"),
    );
    if (key.name === "tab") {
      key.preventDefault();
      const at = available.indexOf(id);
      set(
        available[
          (at + (key.shift ? -1 : 1) + available.length) % available.length
        ] || "",
      );
      return;
    }
    const entry = entries.get(id);
    if ((key.name === "return" || key.name === "space") && entry?.action) {
      key.preventDefault();
      entry.action();
    }
    const scroll = entry?.scroll?.();
    if (scroll && ["down", "up", "pagedown", "pageup"].includes(key.name)) {
      key.preventDefault();
      scroll.scrollBy(
        (key.name === "up" || key.name === "pageup" ? -1 : 1) *
          (key.name.startsWith("page") ? 10 : 2),
      );
    }
  });
  return (
    <Focus.Provider value={{ id, set, entries, modal: Boolean(modal) }}>
      {children}
    </Focus.Provider>
  );
}
export const useFocus = () => useContext(Focus);
export function useFocusable(id: string, entry: Entry) {
  const f = useFocus();
  useEffect(() => {
    f.entries.set(id, entry);
  });
  useEffect(
    () => () => {
      f.entries.delete(id);
    },
    [id],
  );
  return {
    focused: f.id === id,
    active: !f.modal || id.startsWith("modal-"),
    select: () => f.set(id),
  };
}
export function Button({
  id,
  children,
  onPress,
  selected = false,
  primary = false,
  disabled = false,
  width,
  large = false,
}: {
  id: string;
  children: React.ReactNode;
  onPress: () => void;
  selected?: boolean;
  primary?: boolean;
  disabled?: boolean;
  large?: boolean;
  width?: number | `${number}%`;
}) {
  const f = useFocusable(id, { action: disabled ? undefined : onPress });
  const [hover, setHover] = useState(false);
  const armed = useRef(false);
  return (
    <box
      id={id}
      flexDirection="row"
      height={large ? 3 : 1}
      minHeight={large ? 3 : 1}
      alignItems="center"
      flexShrink={0}
      width={width}
      paddingX={1}
      backgroundColor={
        disabled
          ? T.panel
          : primary
            ? T.accent
            : selected || f.focused
              ? T.selection
              : hover
                ? T.raised
                : T.bg
      }
      onMouseOver={() => setHover(true)}
      onMouseOut={() => {
        setHover(false);
        armed.current = false;
      }}
      onMouseDown={(e) => {
        e.stopPropagation();
        if (e.button === 0 && f.active) {
          armed.current = true;
          f.select();
        }
      }}
      onMouseDrag={() => {
        armed.current = false;
      }}
      onMouseUp={(e) => {
        e.stopPropagation();
        if (e.button === 0 && armed.current && !disabled && f.active) onPress();
        armed.current = false;
      }}
    >
      <text
        selectable={false}
        fg={
          disabled
            ? T.dim
            : primary
              ? T.bg
              : selected || f.focused
                ? T.accent
                : T.ink
        }
      >
        {children}
      </text>
    </box>
  );
}
export function Field({
  id,
  value,
  onChange,
  placeholder = "",
  onSubmit,
  secret = false,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  onSubmit?: () => void;
  secret?: boolean;
}) {
  const f = useFocusable(id, { input: true });
  const secretValue = useRef(value);
  secretValue.current = value;
  const setSecret = (next: string) => {
    secretValue.current = next;
    onChange(next);
  };
  useKeyboard((key) => {
    if (!secret || !f.focused || !f.active) return;
    if (key.name === "return") {
      key.preventDefault();
      onSubmit?.();
    } else if (key.name === "backspace") {
      key.preventDefault();
      setSecret(secretValue.current.slice(0, -1));
    } else if (key.ctrl && key.name === "u") {
      key.preventDefault();
      setSecret("");
    } else if (
      !key.ctrl &&
      !key.meta &&
      key.sequence &&
      !/[\x00-\x1f\x7f]/.test(key.sequence)
    ) {
      key.preventDefault();
      setSecret((secretValue.current + key.sequence).slice(0, 8192));
    }
  });
  usePaste((event) => {
    if (secret && f.focused && f.active) {
      event.preventDefault();
      setSecret(
        (
          secretValue.current +
          clean(new TextDecoder().decode(event.bytes)).replace(/\s/g, "")
        ).slice(0, 8192),
      );
    }
  });
  return (
    <box
      id={secret ? id : undefined}
      height={1}
      flexShrink={0}
      backgroundColor={T.raised}
      paddingX={1}
      onMouseDown={(e) => {
        e.stopPropagation();
        if (f.active) f.select();
      }}
    >
      {secret ? (
        <text fg={value ? T.ink : T.dim}>
          {value ? "•".repeat(Math.min(value.length, 48)) : placeholder}
        </text>
      ) : (
        <input
          id={id}
          value={value}
          onInput={(v) => onChange(clean(v, 131072))}
          placeholder={placeholder}
          focused={f.focused && f.active}
          onSubmit={onSubmit}
          maxLength={131072}
          flexGrow={1}
          backgroundColor={T.raised}
          focusedBackgroundColor={T.raised}
          textColor={T.ink}
          focusedTextColor={T.ink}
          placeholderColor={T.dim}
        />
      )}
    </box>
  );
}
export function Scroll({
  id,
  children,
  sticky = false,
}: {
  id: string;
  children: React.ReactNode;
  sticky?: boolean;
}) {
  const ref = useRef<ScrollBoxRenderable>(null),
    f = useFocusable(id, { scroll: () => ref.current });
  return (
    <scrollbox
      id={id}
      ref={ref}
      flexGrow={1}
      minHeight={0}
      width="100%"
      scrollX={false}
      focused={f.focused && f.active}
      stickyScroll={sticky}
      stickyStart={sticky ? "bottom" : "top"}
      onMouseDown={() => f.select()}
      verticalScrollbarOptions={{
        showArrows: false,
        trackOptions: { backgroundColor: T.bg, foregroundColor: T.line },
      }}
    >
      {children}
    </scrollbox>
  );
}
export function Gap({ rows = 1 }: { rows?: number }) {
  return <box height={rows} flexShrink={0} />;
}
export function Rule() {
  return (
    <box height={1} flexShrink={0} border={["bottom"]} borderColor={T.line} />
  );
}
export function Caption({ children }: { children: React.ReactNode }) {
  return (
    <text fg={T.muted} height={1}>
      {children}
    </text>
  );
}

// Native edit buffer preserves pasted newlines and cursor/selection behavior.
export function Composer({
  value,
  onChange,
  onSubmit,
  placeholder,
  compact = false,
}: {
  value: string;
  onChange: (s: string) => void;
  onSubmit: () => void;
  placeholder: string;
  compact?: boolean;
}) {
  const ref = useRef<TextareaRenderable>(null),
    f = useFocusable("composer", { input: true });
  useEffect(() => {
    if (ref.current && ref.current.plainText !== value)
      ref.current.setText(value);
  }, [value]);
  const rows = compact ? 1 : Math.min(4, Math.max(1, value.split("\n").length));
  return (
    <box
      paddingX={1}
      height={rows}
      flexShrink={0}
      onMouseDown={() => {
        if (f.active) f.select();
      }}
    >
      <textarea
        id="composer"
        ref={ref}
        initialValue={value}
        focused={f.focused && f.active}
        height={rows}
        width="100%"
        wrapMode="word"
        backgroundColor={T.raised}
        focusedBackgroundColor={T.raised}
        textColor={T.ink}
        focusedTextColor={T.ink}
        placeholderColor={T.dim}
        placeholder={placeholder}
        keyBindings={[
          { name: "return", action: "submit" },
          { name: "return", shift: true, action: "newline" },
          { name: "j", ctrl: true, action: "newline" },
          { name: "linefeed", action: "newline" },
        ]}
        onContentChange={() => {
          const raw = ref.current?.plainText || "",
            next = clean(raw, 131072);
          if (raw !== next) ref.current?.setText(next);
          onChange(next);
        }}
        onSubmit={onSubmit}
      />
    </box>
  );
}
