// File-type icons (Catppuccin, web/src/icons): which icon a path shows, and the element that
// draws it. The sprite is mounted once; every icon is a <use> of one of its symbols, so a row
// costs one small element whatever the icon.

import sprite from "./icons/sprite.svg";
import { EXTENSIONS, FILE_NAMES } from "./icons/associations";

const SVG_NS = "http://www.w3.org/2000/svg";

/**
 * The icon for a path: its file name, else its longest extension (`a.test.ts` tries `test.ts`,
 * then `ts`), else the plain file icon. Case never matters.
 */
export function iconOf(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1).toLowerCase();
  const byName = FILE_NAMES[name];
  if (byName) return byName;
  // A leading dot starts a name (.env), not an extension; the name lookup above covers it.
  for (let at = name.indexOf(".", 1); at >= 0; at = name.indexOf(".", at + 1)) {
    const icon = EXTENSIONS[name.slice(at + 1)];
    if (icon) return icon;
  }
  return "_file";
}

/** Mounts the sprite into the document once, before any icon is drawn. */
export function mountIcons(): void {
  const doc = new DOMParser().parseFromString(sprite, "image/svg+xml");
  document.body.prepend(document.importNode(doc.documentElement, true));
}

/** A decorative 16px icon; the row's text names the file. */
export function icon(name: string): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", "ficon");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS(SVG_NS, "use");
  use.setAttribute("href", `#i-${name}`);
  svg.append(use);
  return svg;
}

/** The icon of a changed file's path. */
export function fileIcon(path: string): SVGSVGElement {
  return icon(iconOf(path));
}

/** A directory's icon, open while expanded. */
export function folderIcon(open: boolean): SVGSVGElement {
  return icon(open ? "_folder_open" : "_folder");
}
