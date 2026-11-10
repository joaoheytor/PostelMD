import { App } from "./core/app";
import { browserDrafts } from "./core/drafts-idb";
import "./styles.css";
import "./ui/app";
import type { AppView } from "./ui/app";

const app = new App();
const view = document.createElement("pm-app") as AppView;
view.app = app;
document.querySelector("#app")?.replaceWith(view);
void app.boot(browserDrafts);
