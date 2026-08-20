import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { resetSelection } from "../lib/store";
import { createNote, saveNote, throwAwayNote } from "../lib/actions";
import type { StickyNote } from "../lib/types";
import { ListSkeleton, PageHead } from "../ui/bits";
import { Icon } from "../ui/Icon";

const PALETTE = 5;

/** Stable tiny tilt per note — reads as "stuck on a wall" without gimmick. */
function tiltFor(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return ((h % 9) - 4) * 0.35;
}

function Sticky({ note, pos, editing, onEdit, onMoved, onRemoved }: {
  note: StickyNote;
  pos: { x: number; y: number };
  editing: boolean;
  onEdit: (id: string | null) => void;
  onMoved: (id: string, x: number, y: number) => void;
  onRemoved: () => void;
}) {
  const [text, setText] = useState(note.text);
  const [color, setColor] = useState(note.color);

  useEffect(() => { setText(note.text); setColor(note.color); }, [note]);

  const startDrag = (e: PointerEvent) => {
    if ((e.target as HTMLElement).closest("textarea, button")) return;
    const startX = e.clientX, startY = e.clientY;
    const origX = pos.x, origY = pos.y;
    let lastX = origX, lastY = origY;
    let moved = false;
    const move = (ev: PointerEvent) => {
      const dx = ev.clientX - startX, dy = ev.clientY - startY;
      if (!moved && Math.abs(dx) + Math.abs(dy) < 4) return;
      moved = true;
      lastX = Math.max(0, origX + dx);
      lastY = Math.max(0, origY + dy);
      const el = document.getElementById("sticky-" + note.id);
      if (el) { el.style.left = lastX + "px"; el.style.top = lastY + "px"; }
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      if (moved) onMoved(note.id, lastX, lastY);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  const commitText = () => {
    onEdit(null);
    if (text !== note.text) saveNote(note.id, { text });
  };

  const cycleColor = () => {
    const next = (color + 1) % PALETTE;
    setColor(next);
    saveNote(note.id, { color: next });
  };

  return (
    <div
      id={"sticky-" + note.id}
      class={"sticky sticky-" + color}
      style={{ left: pos.x, top: pos.y, transform: "rotate(" + tiltFor(note.id) + "deg)" }}
      onPointerDown={startDrag}
    >
      <div class="sticky-tools">
        <button class="btn-icon sticky-tool" type="button" title="Change colour" aria-label="Change colour"
          onClick={cycleColor}>
          <span class="sticky-swatch" />
        </button>
        <button class="btn-icon sticky-tool" type="button" title="Throw away" aria-label="Throw away"
          onClick={() => throwAwayNote({ ...note, text, color }, onRemoved)}>
          <Icon name="close" size={13} />
        </button>
      </div>
      {editing ? (
        <textarea
          class="sticky-ta" rows={5} autofocus
          value={text}
          onInput={(ev) => setText((ev.target as HTMLTextAreaElement).value)}
          onBlur={commitText}
          onKeyDown={(ev) => {
            if (ev.key === "Escape") { ev.preventDefault(); (ev.target as HTMLTextAreaElement).blur(); }
            if ((ev.metaKey || ev.ctrlKey) && ev.key === "Enter") { ev.preventDefault(); (ev.target as HTMLTextAreaElement).blur(); }
          }}
        />
      ) : (
        <div class="sticky-text" onClick={() => onEdit(note.id)}>
          {text || <span class="sticky-empty">Write something…</span>}
        </div>
      )}
    </div>
  );
}

export function NotesView() {
  const { data, loading, error, reload } = useLoad<StickyNote[]>("notes", (signal) =>
    api<StickyNote[]>("/notes", { signal })
  );
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({});
  const [editingId, setEditingId] = useState<string | null>(null);
  const wallRef = useRef<HTMLDivElement>(null);

  useEffect(() => { resetSelection(); }, []);

  const posOf = (n: StickyNote) => positions[n.id] || { x: n.x, y: n.y };

  // New notes land just inside wherever the wall is currently scrolled to,
  // nudged along so consecutive drops don't perfectly stack.
  const dropSeq = useRef(0);
  const addNote = async () => {
    const scroller = wallRef.current?.parentElement;
    const baseX = (scroller?.scrollLeft || 0) + 60 + (dropSeq.current % 5) * 36;
    const baseY = (scroller?.scrollTop || 0) + 60 + (dropSeq.current % 5) * 28;
    dropSeq.current++;
    const n = await createNote(baseX, baseY, "", Math.floor(Math.random() * PALETTE));
    if (n) {
      setPositions((p) => ({ ...p, [n.id]: { x: n.x, y: n.y } }));
      setEditingId(n.id);
      reload();
    }
  };

  const notes = data || [];

  return (
    <>
      <div class="notes-topline">
        <div class="page-head">
          <div class="page-kicker">Notes</div>
          <h1 class="page-title">The wall</h1>
          <div class="page-sub">
            Thoughts, not tasks. Double-click anywhere to stick one down, drag it around, throw it away when it's spent.
          </div>
        </div>
        <button class="btn btn-accent" type="button" onClick={addNote}>
          <Icon name="plus" size={14} /><span class="label">Stick a note</span>
        </button>
      </div>

      <div class="canvas-frame">
        {loading && !data && <ListSkeleton rows={3} />}
        {error && <div class="canvas-error">{error}</div>}
        <div class="note-canvas">
          <div
            class="note-wall"
            ref={wallRef}
            onDblClick={(e) => {
              if ((e.target as HTMLElement).closest(".sticky")) return;
              const rect = wallRef.current!.getBoundingClientRect();
              const x = Math.max(0, e.clientX - rect.left - 100);
              const y = Math.max(0, e.clientY - rect.top - 16);
              dropSeq.current++;
              createNote(x, y, "", Math.floor(Math.random() * PALETTE)).then((n) => {
                if (n) {
                  setPositions((p) => ({ ...p, [n.id]: { x: n.x, y: n.y } }));
                  setEditingId(n.id);
                  reload();
                }
              });
            }}
          >
            {notes.length === 0 && !loading && (
              <div class="canvas-hint">Double-click anywhere to stick the first note</div>
            )}
            {notes.map((n) => (
              <Sticky
                key={n.id}
                note={n}
                pos={posOf(n)}
                editing={editingId === n.id}
                onEdit={setEditingId}
                onMoved={(id, x, y) => {
                  setPositions((p) => ({ ...p, [id]: { x, y } }));
                  saveNote(id, { x, y });
                }}
                onRemoved={reload}
              />
            ))}
          </div>
        </div>
      </div>
    </>
  );
}
