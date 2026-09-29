import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { ApprovalApp } from "./App";

const root = document.getElementById("root");
if (!root) {
  throw new Error("approval window root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <ApprovalApp />
  </StrictMode>,
);
