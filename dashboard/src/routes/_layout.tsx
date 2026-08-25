import { Island } from "@neutron-build/core/client";
import App from "../app/App";

export function head() {
  return {
    titleTemplate: "%s — email-soft",
    meta: [
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { name: "theme-color", content: "#15161a" },
      { name: "apple-mobile-web-app-capable", content: "yes" },
      { name: "apple-mobile-web-app-status-bar-style", content: "default" },
    ],
    // Linked explicitly from public/ rather than imported into the module graph.
    // The client bundle is rooted at the islands entry, so a layout-level CSS
    // import never reaches it, and letting the island chunk pull its own
    // stylesheet in would paint one unstyled frame first.
    link: [
      { rel: "stylesheet", href: "/styles.css" },
      { rel: "manifest", href: "/manifest.webmanifest" },
      { rel: "icon", href: "/icon.svg", type: "image/svg+xml" },
      { rel: "apple-touch-icon", href: "/icon-180.png" },
    ],
    // Resolve the theme before the first paint: stored choice wins, else the
    // system preference. Anything later than this flashes.
    headScripts: [
      {
        id: "theme-init",
        content:
          "(function(){try{var t=localStorage.getItem('es-theme');" +
          "if(t!=='light'&&t!=='sepia'&&t!=='dark'){t=(window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches)?'dark':'light';}" +
          "document.documentElement.setAttribute('data-theme',t);}catch(e){}})();",
      },
    ],
  };
}

// One island owns the whole app. Each route below is still a real prerendered
// HTML file, so a cold load or a pasted URL works — but in-app navigation is
// handled inside the island and never tears the document down.
export default function Layout() {
  return <Island component={App} client="load" id="es-app" />;
}
