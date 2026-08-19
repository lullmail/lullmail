export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Feed" };
}

export default function Page() {
  return <div id="view" data-page="bucket" data-bucket="feed"></div>;
}
