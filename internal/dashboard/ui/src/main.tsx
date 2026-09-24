import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { applyAppearance, rememberedAppearance } from "./appearance";
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/ibm-plex-mono/latin-400.css";
import "@fontsource/ibm-plex-mono/latin-500.css";
import "./styles/tokens.css";
import "./styles/base.css";
import "./styles/shell.css";
import "./styles/inbox.css";
import "./styles/projects.css";
import "./styles/project.css";
import "./styles/board.css";
import "./styles/request.css";
import "./styles/chat.css";
import "./styles/pages.css";

applyAppearance(rememberedAppearance());
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
