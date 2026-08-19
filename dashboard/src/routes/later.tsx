export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Later" };
}

export default function Page() {
  return <div id="view" data-page="bucket" data-bucket="later"></div>;
}
