import type { ComponentChildren } from "preact";
import "@fontsource-variable/manrope";
import "@fontsource-variable/newsreader";
import "@fontsource-variable/newsreader/wght-italic.css";
import "../styles/global.css";

export function head() {
  return '<meta name="theme-color" content="#20242b"><link rel="icon" type="image/svg+xml" href="/favicon.svg">';
}

export default function Layout({ children }: { children?: ComponentChildren }) {
  return (
    <>
      {children}
      <script type="module" src="/assets/site.js"></script>
    </>
  );
}
