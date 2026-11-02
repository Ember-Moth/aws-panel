document.addEventListener("htmx:sendError", () => {
  document.getElementById("connection-error")?.classList.remove("d-none");
});
document.addEventListener("htmx:timeout", () => {
  document.getElementById("connection-error")?.classList.remove("d-none");
});
document.addEventListener("htmx:afterRequest", (event) => {
  if (event.detail.successful) {
    document.getElementById("connection-error")?.classList.add("d-none");
  }
});
