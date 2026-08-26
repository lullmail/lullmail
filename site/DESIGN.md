# Release Site Direction

## Thesis

The site should feel finished before it feels impressive. Its quality comes
from restraint, proportion, typography, and truthful product proof rather than
the number of visual devices on screen.

The complete argument is:

1. Mail arrives continuously, but attention can remain finite.
2. Unknown senders are screened once.
3. Today is a briefing with an ending.
4. Unfinished mail can leave and return on its day.
5. Export, deletion, and self-hosting are part of the product.

## Structure

The page has four parts only:

1. **Hero** — one title and one turbulence-to-order scene.
2. **Product** — three proofs: Screener, Today, and timed return.
3. **Trust** — four precise operational guarantees.
4. **Availability** — an honest private-preview status and utility footer.

Anything that does not strengthen one of those four parts is removed.

## Visual System

- Dusk is reserved for the hero. Product content uses warm paper; trust uses
  near-black; availability closes on a slightly deeper paper.
- Newsreader carries editorial titles. Manrope carries interface and body copy.
  Both are bundled WOFF2 variable fonts, so the identity does not depend on the
  visitor's installed fonts.
- The palette is near-black, warm paper, mineral blue, and one burnt-coral
  accent. Coral means attention or current status, not decoration.
- Product frames follow the actual application's document-first visual system:
  quiet chrome, serif subjects, hairline separators, and almost no shadow.
- Sentence case is the default. Small uppercase text is reserved for status and
  protocol-like metadata.

## Motion

- WebGL exists only in the desktop hero. Instanced envelope planes move from
  turbulent depth into a sparse aligned flow as the visitor scrolls.
- There is no portal, character, torus, dust field, pointer parallax, gust
  control, orbit, second canvas, or perpetual animation outside the hero.
- The desktop timeline is 170svh. Mobile, reduced-motion, no-JavaScript, and
  failed-WebGL states use a deliberately composed 100svh poster.
- Product and trust content remain still. The hero is the only animated
  narrative on the page.

## Performance And Fallbacks

- Largest-contentful paint is always the HTML title over the CSS poster.
- Three.js loads two animation frames after the page script and only on desktop
  when reduced motion is not requested.
- JavaScript remains below 250 KB gzip, including Three.js.
- Geometry is one InstancedMesh with 42 planes. DPR is capped at 1.5 and drops
  to 1 after sustained slow frames. Rendering pauses offscreen and when hidden.
- Without JavaScript, every section and the complete hero poster remain visible.
- WebGL context loss collapses the hero back to its static poster height.

## Acceptance Bar

- The first frame works as a finished poster.
- The product is understood from the page without interpreting metaphors.
- Every claim maps to shipped behavior in the repository.
- Mobile is a distinct static composition, not a cropped desktop timeline.
- There are no inactive controls, decorative dashboards, horizontal scroll
  traps, or visual labels explaining the art direction.
