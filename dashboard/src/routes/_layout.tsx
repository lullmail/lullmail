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
          "(function(){try{var d=document.documentElement;" +
          "var ok=['light','sepia','dark','terminal','amber','nord','dracula','rose','solarized','blueprint','y2k','1999','vapor'];" +
          "var t=localStorage.getItem('es-theme');" +
          "if(ok.indexOf(t)<0){t=(window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches)?'dark':'light';}" +
          "d.setAttribute('data-theme',t);" +
          "var a=localStorage.getItem('es-accent');" +
          "if(a){d.setAttribute('data-accent',a);}" +
          "var y=localStorage.getItem('es-type');" +
          "if(y==='clean'){d.setAttribute('data-type','sans');}" +
          "['textsize','density','measure'].forEach(function(k){var v=localStorage.getItem('es-'+k);if(v){d.setAttribute('data-'+k,v);}});" +
          "var cx=localStorage.getItem('es-accent-custom');" +
          "if(a==='custom'&&/^#[0-9a-fA-F]{6}$/.test(cx||'')){" +
          "var n=parseInt(cx.slice(1),16),r=(n>>16)&255,g=(n>>8)&255,b=n&255;" +
          "d.style.setProperty('--accent',cx);" +
          "d.style.setProperty('--accent-ink',(0.2126*r+0.7152*g+0.0722*b)>150?'#111111':'#ffffff');" +
          "d.style.setProperty('--accent-soft','color-mix(in srgb '+cx+' 12%, transparent)');}" +
          "}catch(e){}})();",
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
