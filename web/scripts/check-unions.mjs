// Fails when a union type is written inline instead of behind a named type alias. Two unions may
// stay inline: one type with null or undefined, which is optionality, not a union of values; and
// the key list of Pick or Omit, which names members of a type, not values.
import { API } from "typescript/unstable/sync";
import { SyntaxKind } from "typescript/unstable/ast";
import { resolve } from "node:path";

const nullish = new Set([SyntaxKind.NullKeyword, SyntaxKind.UndefinedKeyword]);
const isNullish = (t) => nullish.has(t.kind) || (t.kind === SyntaxKind.LiteralType && t.literal.kind === SyntaxKind.NullKeyword);

const keyLists = new Set(["Pick", "Omit"]);
const isKeyList = (parent) =>
  parent.kind === SyntaxKind.TypeReference && parent.typeName.kind === SyntaxKind.Identifier && keyLists.has(parent.typeName.text);

const config = resolve(import.meta.dirname, "../tsconfig.json");
// Only the project's own sources; the program also holds TypeScript's lib files, wherever the checkout is.
const src = resolve(import.meta.dirname, "../src") + "/";
const api = new API({ cwd: resolve(import.meta.dirname, "..") });
const problems = [];
try {
  const snapshot = api.updateSnapshot({ openProjects: [config] });
  const program = snapshot.getProject(config).program;
  for (const name of program.getSourceFileNames()) {
    if (!name.startsWith(src)) continue;
    const file = program.getSourceFile(name);
    const visit = (node, parent) => {
      if (node.kind === SyntaxKind.UnionType && parent.kind !== SyntaxKind.TypeAliasDeclaration && !isKeyList(parent)) {
        const values = node.types.filter((t) => !isNullish(t));
        if (values.length > 1) problems.push(`${name}:${line(file, node.pos)}: inline union; name it with a type alias`);
      }
      node.forEachChild((child) => visit(child, node));
    };
    file.forEachChild((child) => visit(child, file));
  }
} finally {
  api.close();
}

function line(file, pos) {
  const text = file.text;
  let n = 1;
  for (let i = 0; i < pos; i++) if (text.charCodeAt(i) === 10) n++;
  // pos includes leading trivia; count to the first non-space character.
  for (let i = pos; i < text.length && /\s/.test(text[i]); i++) if (text.charCodeAt(i) === 10) n++;
  return n;
}

for (const p of problems) console.error(p);
process.exit(problems.length === 0 ? 0 : 1);
