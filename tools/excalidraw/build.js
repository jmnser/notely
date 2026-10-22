// Bundles index.js with Excalidraw and React into cmd/notely-server/web/excalidraw.
import { build } from "esbuild";
import { cpSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";

const out = "../../cmd/notely-server/web/excalidraw";
const pkg = "node_modules/@excalidraw/excalidraw";

// stubs leaves out Mermaid to Excalidraw, which would bundle Mermaid a second
// time, and the translations: Notely is in English.
const stubs = {
  name: "stubs",
  setup(b) {
    const stub = (args) => (/\/en-[^/]+$/.test(args.path) ? undefined : { path: args.path, namespace: "stub" });
    b.onResolve({ filter: /^@excalidraw\/mermaid-to-excalidraw$|\/locales\/[^/]+\.js$/ }, stub);
    b.onLoad({ filter: /mermaid/, namespace: "stub" }, () => ({
      contents: `export const parseMermaidToExcalidraw = () => { throw new Error("not available"); };`,
    }));
    b.onLoad({ filter: /locales/, namespace: "stub" }, () => ({ contents: "export default {};" }));
  },
};

// noCDN drops the CDN that Excalidraw adds as the last source of each font:
// Notely makes no third-party requests.
const noCDN = {
  name: "no-cdn",
  setup(b) {
    b.onLoad({ filter: /@excalidraw[\\/]excalidraw[\\/]dist[\\/]prod[\\/].*\.js$/ }, (args) => {
      const src = readFileSync(args.path, "utf8");
      const out = src.replace(/(\w+)\.push\(new URL\(\w+,\w+\.ASSETS_FALLBACK_URL\)\),\1/, "$1");
      fallbackRemoved ||= out !== src;
      return { contents: out, loader: "js" };
    });
    b.onEnd(() => {
      if (!fallbackRemoved) throw new Error("font CDN fallback not found");
    });
  },
};
let fallbackRemoved = false;

rmSync(out, { recursive: true, force: true });
const { metafile } = await build({
  metafile: true,
  entryPoints: ["index.js"],
  bundle: true,
  splitting: true,
  format: "esm",
  minify: true,
  target: "es2022",
  outdir: out,
  entryNames: "[name]",
  chunkNames: "chunks/[name]-[hash]",
  define: { "process.env.NODE_ENV": '"production"' },
  conditions: ["production"],
  external: ["*.woff2"], // the copied fonts
  legalComments: "none",
  plugins: [stubs, noCDN],
});
// Xiaolai (CJK, 13 MB) is left out; CJK text uses a system font.
cpSync(`${pkg}/dist/prod/fonts`, `${out}/fonts`, {
  recursive: true,
  filter: (src) => !src.includes("Xiaolai"),
});
// LICENSE lists every bundled package with its license text.
const packages = new Set();
for (const input of Object.keys(metafile.inputs)) {
  const i = input.lastIndexOf("node_modules/") + "node_modules/".length;
  if (i < "node_modules/".length) continue;
  const [scope, name] = input.slice(i).split("/");
  packages.add(input.slice(0, i) + (scope.startsWith("@") ? `${scope}/${name}` : scope));
}
packages.add(pkg); // the fonts
const notices = new Map();
for (const dir of packages) {
  const { name, version, license } = JSON.parse(readFileSync(`${dir}/package.json`, "utf8"));
  const file = readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  const text = file ? readFileSync(`${dir}/${file}`, "utf8").trim() : "";
  notices.set(`${name}@${version}`, `## ${name}@${version} (${license ?? "see package"})\n\n${text}\n`);
}
writeFileSync(`${out}/LICENSE`, [...notices.keys()].sort().map((k) => notices.get(k)).join("\n"));
