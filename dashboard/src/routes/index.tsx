export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Imbox" };
}

export default function Page() {
  return <div id="view" data-page="bucket" data-bucket="imbox"></div>;
}
