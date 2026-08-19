export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — Today" };
}

export default function Page() {
  return <div id="view" data-page="today"></div>;
}
