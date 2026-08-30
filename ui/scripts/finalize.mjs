import { readFile, rm, writeFile } from "node:fs/promises";

const source = new URL("../../internal/quota/status.source.html", import.meta.url);
const target = new URL("../../internal/quota/status.html", import.meta.url);
const scriptTarget = new URL("../../internal/quota/status.js", import.meta.url);
const generated = await readFile(source, "utf8");
const match = generated.match(/<script type="module" crossorigin>([\s\S]*?)<\/script>/);
if (!match) throw new Error("Vite bundle script was not found");
await writeFile(scriptTarget, match[1], "utf8");
const html = generated.replace(match[0], '<script src="/v0/resource/plugins/codex-quota-activation/status.js" defer></script>');
await rm(target, { force: true });
await writeFile(target, html, "utf8");
await rm(source, { force: true });
