export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Paper Trail" };
}

export default function Page() {
  return <div id="view" data-page="bucket" data-bucket="paper_trail"></div>;
}
