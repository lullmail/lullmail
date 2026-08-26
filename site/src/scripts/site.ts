const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
const desktop = window.matchMedia("(min-width: 701px)").matches;

if (!reduced && desktop) {
  requestAnimationFrame(() => requestAnimationFrame(() => import("./cinematic")));
}

const header = document.querySelector("[data-header]");
const product = document.querySelector("#product");
const syncHeader = () => {
  if (!header || !product) return;
  header.classList.toggle("on-paper", product.getBoundingClientRect().top <= 72);
};

syncHeader();
addEventListener("scroll", syncHeader, { passive: true });
addEventListener("resize", syncHeader, { passive: true });
