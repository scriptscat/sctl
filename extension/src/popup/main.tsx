import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";

const root = document.getElementById("root");
if (!root) {
  throw new Error("popup root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <main className="w-80 p-4 text-sm">sctl Browser</main>
  </StrictMode>,
);
