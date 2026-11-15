const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

if (!reduced) {
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

// Copy buttons on the install commands. The label is the only feedback: it
// says what happened and goes back.
for (const button of document.querySelectorAll<HTMLButtonElement>("[data-copy]")) {
  button.addEventListener("click", async () => {
    const original = button.textContent;
    try {
      await navigator.clipboard.writeText(button.dataset.copy || "");
      button.textContent = "Copied";
    } catch {
      button.textContent = "Select and copy";
    }
    setTimeout(() => { button.textContent = original; }, 1600);
  });
}
