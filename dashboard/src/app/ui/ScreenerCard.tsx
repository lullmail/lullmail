import type { ScreenerSender } from "../lib/types";
import { cursor } from "../lib/store";
import { decide } from "../lib/actions";
import { countOf, splitFrom } from "../lib/fmt";
import { Avatar } from "./bits";

/** The one Screener card. Today embedded a second copy with a different button
    set and a different order; a decision this permanent must look and behave
    identically wherever it is made. */
const CHOICES: [string, boolean, "imbox" | "feed" | "paper_trail" | "blocked", string][] = [
  ["Imbox", true, "imbox", "btn-primary"],
  ["Reading", true, "feed", "btn-outline"],
  ["Receipts", true, "paper_trail", "btn-outline"],
  // Quiet until hovered: colouring the destructive choice by default pulls the
  // eye straight to it, next to three safe ones.
  ["Block", false, "blocked", "btn-quiet-danger"],
];

export function ScreenerCard({ sender, index }: { sender: ScreenerSender; index: number }) {
  const who = splitFrom(sender.sender);
  const named = who.name && who.name !== who.email;

  return (
    <div
      class={"screener" + (index >= 0 && cursor.value === index ? " cursor" : "")}
      data-cursor-index={index}
      onClick={() => { if (index >= 0) cursor.value = index; }}
    >
      <div class="screener-id">
        <Avatar email={who.email} name={who.name} size="lg" />
        <div class="screener-main">
          <div class="screener-name">{named ? who.name : who.email}</div>
          {named && <div class="screener-addr">{who.email}</div>}
          <div class="screener-sample">
            <span class="chip">{countOf(sender.waiting, "message")} waiting</span>
            {sender.sample_subject && <span class="screener-subject">{sender.sample_subject}</span>}
          </div>
        </div>
      </div>

      <div class="screener-btns">
        {CHOICES.map(([label, allow, route, cls]) => (
          <button
            class={"btn " + cls} type="button" key={route}
            onClick={() => decide(sender.sender, allow, route)}
          >
            {label}
          </button>
        ))}
      </div>
    </div>
  );
}
