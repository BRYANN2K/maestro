import React from "react";
import { T, clean } from "./theme";

type LinkPart = { text: string; href?: string };

// Remove supplied terminal escapes before generating our own hyperlinks.
export function browserURL(value: string): string | null {
  if (!/^https?:\/\//i.test(value) || /[\s\p{Cc}\p{Cf}]/u.test(value))
    return null;
  try {
    const url = new URL(value);
    return url.hostname && !url.username && !url.password ? value : null;
  } catch {
    return null;
  }
}

export function linkParts(text: string): LinkPart[] {
  const value = clean(text);
  const pattern = /\[([^\]\n]+)\]\((https?:\/\/(?:[^\s<>()]|\([^\s<>()]*\))+)\)|https?:\/\/[^\s<>"`]+/gi;
  const parts: LinkPart[] = [];
  let at = 0;
  for (const match of value.matchAll(pattern)) {
    if (match.index! > at)
      parts.push({ text: value.slice(at, match.index) });
    let destination = match[2] || match[0];
    if (!match[2]) {
      destination = destination.replace(/[.,;:!?]+$/, "");
      const extraClosing = Math.max(
        0,
        (destination.match(/\)/g)?.length || 0) -
          (destination.match(/\(/g)?.length || 0),
      );
      const trailingClosing = destination.match(/\)+$/)?.[0].length || 0;
      destination = destination.slice(0, destination.length - Math.min(extraClosing, trailingClosing));
      destination = destination.replace(/[\]}]+$/, "");
    }
    const href = browserURL(destination);
    parts.push(href
      ? { text: match[1] || destination, href }
      : { text: match[0] });
    if (href && !match[2] && destination.length < match[0].length)
      parts.push({ text: match[0].slice(destination.length) });
    at = match.index! + match[0].length;
  }
  if (at < value.length) parts.push({ text: value.slice(at) });
  return parts;
}

export function LinkedText({ text }: { text: string }) {
  return <>{linkParts(text).map((part, i) => part.href ? (
    <a key={`${i}:${part.href}`} href={part.href} fg={T.accent}>
      <u>{part.text}</u>
    </a>
  ) : <span key={i}>{part.text}</span>)}</>;
}
