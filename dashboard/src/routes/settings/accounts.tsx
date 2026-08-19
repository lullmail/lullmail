export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Accounts" };
}

export default function Page() {
  return <div id="view" data-page="accounts"></div>;
}
