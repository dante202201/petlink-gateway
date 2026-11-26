import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import AdminApp from "./AdminApp";
import "./styles.css";

/** /admin、/admin.html，以及旧书签 #admin / #firmware 都进后台，不依赖 Vite 是否 rewrite。 */
function isAdminEntry() {
  if (typeof location === "undefined") return false;
  const path = (location.pathname || "/").replace(/\/+$/, "") || "/";
  if (path === "/admin" || path.endsWith("/admin") || path.endsWith("/admin.html")) return true;
  const h = (location.hash || "").replace(/^#/, "");
  return h === "admin" || h === "firmware" || h === "history" || h === "ota" || h === "devices" || h === "logs" || h === "online";
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {isAdminEntry() ? <AdminApp /> : <App />}
  </StrictMode>,
);
