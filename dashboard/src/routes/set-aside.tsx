export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Set Aside" };
}

export default function Page() {
  return <div id="view" data-page="bucket" data-bucket="set_aside"></div>;
}
