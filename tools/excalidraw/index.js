// Notely's bridge to Excalidraw, set as window.notelyExcalidraw. Scenes are
// passed as the JSON of a .excalidraw file.
import { createElement as h } from "react";
import { createRoot } from "react-dom/client";
import {
  CaptureUpdateAction,
  Excalidraw,
  MainMenu,
  exportToSvg,
  getSceneVersion,
  restore,
  serializeAsJSON,
} from "@excalidraw/excalidraw";
import "@excalidraw/excalidraw/index.css";

const parse = (json) => {
  try {
    return restore(json ? JSON.parse(json) : null, null, null);
  } catch {
    return restore(null, null, null);
  }
};

// version changes with the elements and the saved canvas settings.
const version = (elements, appState, files) =>
  [getSceneVersion(elements), appState.viewBackgroundColor, appState.gridModeEnabled,
    appState.gridSize, Object.keys(files).length].join("|");

const menu = h(MainMenu, null,
  h(MainMenu.DefaultItems.LoadScene),
  h(MainMenu.DefaultItems.Export),
  h(MainMenu.DefaultItems.SaveAsImage),
  h(MainMenu.DefaultItems.SearchMenu),
  h(MainMenu.DefaultItems.Help),
  h(MainMenu.DefaultItems.ClearCanvas),
  h(MainMenu.Separator),
  h(MainMenu.DefaultItems.ChangeCanvasBackground));

// mount shows an editor for json in el. onChange gets the scene's JSON once
// changes pause. It returns update, to show a scene changed elsewhere, and
// unmount, which saves pending changes first.
function mount(el, json, dark, onChange) {
  const root = createRoot(el);
  const data = parse(json);
  let api, sent = json, base = null, timer = 0;
  const save = () => {
    clearTimeout(timer);
    timer = 0;
    if (!api) return;
    sent = serializeAsJSON(api.getSceneElementsIncludingDeleted(), api.getAppState(), api.getFiles(), "local");
    onChange(sent);
  };
  const render = (dark) => root.render(h(Excalidraw, {
    initialData: { elements: data.elements, appState: data.appState, files: data.files, scrollToContent: true },
    excalidrawAPI: (a) => { api = a; },
    onChange: (elements, appState, files) => {
      const v = version(elements, appState, files);
      if (base === null) base = v; // the loaded scene
      if (v === base) return;
      base = v;
      clearTimeout(timer);
      timer = setTimeout(save, 300);
    },
    theme: dark ? "dark" : "light",
    langCode: "en",
    autoFocus: true,
    aiEnabled: false,
    validateEmbeddable: () => false, // no third-party frames
    UIOptions: {
      canvasActions: { toggleTheme: false, saveToActiveFile: false },
      tools: { image: false }, // images would make notes large
    },
  }, menu));
  render(dark);
  return {
    theme: render,
    update(json) {
      if (json === sent || !api) return;
      const d = parse(json);
      sent = json;
      base = null;
      clearTimeout(timer);
      timer = 0;
      api.addFiles(Object.values(d.files));
      api.updateScene({
        elements: d.elements,
        appState: { viewBackgroundColor: d.appState.viewBackgroundColor },
        captureUpdate: CaptureUpdateAction.NEVER,
      });
    },
    unmount() {
      if (timer) save();
      root.unmount();
    },
  };
}

// svg renders json as an SVG element, or null if it has nothing to show.
async function svg(json, dark) {
  const d = parse(json);
  const elements = d.elements.filter((e) => !e.isDeleted);
  if (!elements.length) return null;
  return exportToSvg({
    elements,
    appState: { ...d.appState, exportBackground: false, exportWithDarkMode: dark },
    files: d.files,
    exportPadding: 16,
    renderEmbeddables: false,
  });
}

// embed fills el with json's SVG, unless el.dataset.key changes meanwhile.
async function embed(el, json, dark) {
  const key = el.dataset.key;
  let content;
  try {
    content = (await svg(json, dark)) ?? "Empty drawing";
  } catch (err) {
    console.error(err);
    content = "Can't show this drawing";
  }
  if (el.dataset.key === key) el.replaceChildren(content);
}

window.notelyExcalidraw = { mount, embed };
