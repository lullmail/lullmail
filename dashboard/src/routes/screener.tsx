export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Screener" };
}

export default function Page() {
  return <div id="view" data-page="screener"></div>;
}
