export const config = { mode: "static" };

export function head() {
  return { title: "email-soft — People" };
}

export default function Page() {
  return <div id="view" data-page="people"></div>;
}
