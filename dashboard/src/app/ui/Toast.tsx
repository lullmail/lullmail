import { dismissToast, toast } from "../lib/store";
import { Icon } from "./Icon";

export function Toast() {
  const t = toast.value;
  if (!t) return null;
  return <div class={"toast" + (t.tone === "error" ? " error" : "")} role={t.tone === "error" ? "alert" : "status"} aria-live={t.tone === "error" ? "assertive" : "polite"}>
    <span class="toast-msg">{t.message}</span>
    {t.undo && (
      <button class="btn btn-sm" type="button" onClick={() => { t.undo!(); dismissToast(); }}>
        <Icon name="undo" size={13} /> Undo
      </button>
    )}
  </div>;
}
