export const config = { mode: "static" };

export function head() {
  return { title: "Inbox" };
}

// The shell for this URL. Every pixel is rendered by the App island in the
// layout; this exists so the route prerenders to a real file.
export default function Page() {
  return null;
}
