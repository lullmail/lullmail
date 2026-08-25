import { signal } from "@preact/signals";

interface InstallPromptEvent extends Event {
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

let promptEvent: InstallPromptEvent | null = null;

export const offline = signal(false);
export const installKind = signal<"native" | "ios" | null>(null);

function standalone(): boolean {
  const nav = navigator as Navigator & { standalone?: boolean };
  return !!nav.standalone || window.matchMedia("(display-mode: standalone)").matches;
}

/** Register the offline shell and expose the platform's install affordance. */
export function startPWA(): () => void {
  const updateNetwork = () => { offline.value = !navigator.onLine; };
  const beforeInstall = (event: Event) => {
    event.preventDefault();
    promptEvent = event as InstallPromptEvent;
    installKind.value = "native";
  };
  const installed = () => {
    promptEvent = null;
    installKind.value = null;
  };

  updateNetwork();
  window.addEventListener("online", updateNetwork);
  window.addEventListener("offline", updateNetwork);
  window.addEventListener("beforeinstallprompt", beforeInstall);
  window.addEventListener("appinstalled", installed);

  if (!standalone() && /iphone|ipad|ipod/i.test(navigator.userAgent)) {
    installKind.value = "ios";
  }
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/service-worker.js").catch(() => {
      // The app remains usable online; installability is progressive.
    });
  }

  return () => {
    window.removeEventListener("online", updateNetwork);
    window.removeEventListener("offline", updateNetwork);
    window.removeEventListener("beforeinstallprompt", beforeInstall);
    window.removeEventListener("appinstalled", installed);
  };
}

export async function installApp(): Promise<boolean> {
  if (!promptEvent) return false;
  await promptEvent.prompt();
  const choice = await promptEvent.userChoice;
  if (choice.outcome === "accepted") {
    promptEvent = null;
    installKind.value = null;
    return true;
  }
  return false;
}
