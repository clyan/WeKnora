const fs = require('node:fs');
const ts = require(process.argv[1]);
if (ts.version !== '6.0.3') throw new Error('requires TypeScript 6.0.3');
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const source = input.source;
const file = ts.createSourceFile(input.path, source, ts.ScriptTarget.Latest, true,
  input.path.endsWith('.json') ? ts.ScriptKind.JSON : undefined);
const output = { version: 'typescript-6.0.3/v2', source, kind: 'implementation', reason: '', nodes: [], diagnostics: [] };
const ranges = new Map();
const literals = [];
let generatedGlyphBytes = 0;
let moduleOrJSX = file.statements.some(node => !ts.isExpressionStatement(node) && !ts.isEmptyStatement(node));
const mask = (start, end, comment = false) => ranges.set(`${start}:${end}`, { start, end, comment });
const comments = (offset, trailing) => {
  const get = trailing ? ts.getTrailingCommentRanges : ts.getLeadingCommentRanges;
  for (const range of get(source, offset) || []) mask(range.pos, range.end, true);
};
const line = offset => file.getLineAndCharacterOfPosition(offset).line + 1;
const text = node => node ? source.slice(node.getStart(file), node.end) : '';
const add = (kind, node, data) => {
  const start = node.getStart(file);
  const enclosing = [];
  let owner = '';
  for (let child = node, parent = node.parent; parent; child = parent, parent = parent.parent) {
    if (!owner && (ts.isFunctionDeclaration(parent) || ts.isMethodDeclaration(parent) || ts.isArrowFunction(parent) || ts.isFunctionExpression(parent))) {
      owner = text(parent.name || parent.parent?.name) || `${parent.getStart(file)}:${parent.end}`;
    }
    if (ts.isIfStatement(parent) && child !== parent.expression) {
      enclosing.push({expression: text(parent.expression), branch: child === parent.elseStatement ? 'false' : 'true'});
    }
    if (ts.isConditionalExpression(parent) && child !== parent.condition) {
      enclosing.push({expression: text(parent.condition), branch: child === parent.whenTrue ? 'true' : 'false'});
    }
    if (ts.isBinaryExpression(parent) && child === parent.right && [ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken].includes(parent.operatorToken.kind)) {
      enclosing.push({expression: text(parent.left), branch: parent.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken ? 'truthy' : parent.operatorToken.kind === ts.SyntaxKind.BarBarToken ? 'falsy' : 'nullish'});
    }
  }
  output.nodes.push({kind, start_offset: start, end_offset: node.end, start_line: line(start), end_line: line(node.end), owner, enclosing, ...data});
};
function visit(node) {
  if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node) ||
      (ts.getCombinedModifierFlags(node) & ts.ModifierFlags.Export) || ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) moduleOrJSX = true;
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
      ts.isPropertyAccessExpression(node.left) && text(node.left.expression) === 'window' &&
      !/^_iconfont_svg_string_/.test(node.left.name.text)) moduleOrJSX = true;
  if (ts.isCallExpression(node) && /^(fetch|request|axios)(\.|$)/.test(text(node.expression))) moduleOrJSX = true;
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
      ts.isPropertyAccessExpression(node.left) && text(node.left.expression) === 'window' &&
      /^_iconfont_svg_string_/.test(node.left.name.text) && ts.isStringLiteralLike(node.right) &&
      node.right.text.includes('<svg') && node.right.text.includes('<symbol')) {
    generatedGlyphBytes += node.right.text.length;
  }
  // Trivia scanners do not know JSX/template lexical context. Literal text
  // containing // or /* must never be mistaken for explanatory comments.
  if (ts.isJsxText(node) || ts.isStringLiteralLike(node) || ts.isRegularExpressionLiteral(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
    literals.push({start: ts.isJsxText(node) ? node.pos : node.getStart(file), end: node.end});
  }
  comments(node.pos, false); comments(node.end, true);
  if (ts.isImportDeclaration(node)) {
    const module = node.moduleSpecifier.text;
    if (/^(msw(?:\/|$)|@faker-js\/|faker$|vitest$|jest$|axios-mock-adapter$|mockjs$)/.test(module)) {
      output.kind = 'mock'; output.reason = `模拟或测试依赖：${module}`;
    }
    add('import', node, {module, bindings: text(node.importClause)});
  }
  if ((ts.isFunctionDeclaration(node) || ts.isVariableDeclaration(node)) && node.name && /^createMock[A-Z]|^mock[A-Z]/.test(text(node.name))) {
    output.kind = 'uncertain'; output.reason = '实现包含模拟工厂/数据声明，需确认来源角色';
  }
  if (ts.isExportDeclaration(node)) add('export', node, {module: node.moduleSpecifier?.text || '', bindings: text(node.exportClause)});
  if ((ts.isVariableStatement(node) || ts.isFunctionDeclaration(node) || ts.isClassDeclaration(node) || ts.isInterfaceDeclaration(node) || ts.isTypeAliasDeclaration(node)) && (ts.getCombinedModifierFlags(node) & ts.ModifierFlags.Export)) {
    add('export', node, {module: '', name: (ts.getCombinedModifierFlags(node) & ts.ModifierFlags.Default) ? 'default' : text(node.name), bindings: text(node.name || node.declarationList)});
  }
  if (ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) || ts.isArrowFunction(node) || ts.isFunctionExpression(node)) {
    add('function', node, {name: text(node.name || node.parent?.name), parameters: node.parameters.map(text)});
  }
  if (ts.isCallExpression(node)) add('call', node, {callee: text(node.expression), arguments: node.arguments.map(text)});
  if (ts.isVariableDeclaration(node)) add('binding', node, {name: text(node.name), initializer: text(node.initializer)});
  if (ts.isObjectLiteralExpression(node)) add('object', node, {properties: node.properties.map(p => ({name: text(p.name), value: ts.isSpreadAssignment(p) ? text(p.expression) : ts.isShorthandPropertyAssignment(p) ? text(p.name) : text(p.initializer), spread: ts.isSpreadAssignment(p)}))});
  if (ts.isIfStatement(node)) add('condition', node, {expression: text(node.expression)});
  if (ts.isConditionalExpression(node)) add('condition', node, {expression: text(node.condition), when_true: text(node.whenTrue), when_false: text(node.whenFalse)});
  if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
    add('component', node, {name: text(node.tagName), properties: node.attributes.properties.map(p => ({name: text(p.name), value: ts.isJsxSpreadAttribute(p) ? text(p.expression) : text(p.initializer), spread: ts.isJsxSpreadAttribute(p)}))});
  }
  if (input.path.endsWith('.json') && ts.isPropertyAssignment(node) && ['description', 'readme', 'author', 'authors', 'contributors'].includes(node.name.text)) {
    mask(node.initializer.getStart(file), node.initializer.end);
  }
  // Include punctuation tokens: comments in empty objects/blocks/JSX
  // expressions have no semantic child for forEachChild to visit.
  for (const child of node.getChildren(file)) visit(child);
}
visit(file);
if (output.kind === 'implementation' && !moduleOrJSX && generatedGlyphBytes > 4096 && generatedGlyphBytes > source.length / 2) {
  output.kind = 'asset'; output.reason = '生成的 SVG 图标字形资源；不展开资源内容，引用和交互从使用方实现取证';
}
comments(file.endOfFileToken.pos, false);
const chars = source.split('');
for (const {start, end, comment} of ranges.values()) {
  if (comment && literals.some(literal => start < literal.end && end > literal.start)) continue;
  for (let i = start; i < end; i++) if (chars[i] !== '\n' && chars[i] !== '\r') chars[i] = ' ';
}
output.source = chars.join('');
// Candidate expressions must use the same filtered view; raw getText may contain comments.
// Re-extract after sanitization so comments cannot leak through nested expression text.
if (!input.filtered) {
  // The Go host submits the filtered view for candidate extraction.
  output.nodes = [];
}
output.diagnostics = file.parseDiagnostics.map(d => ({start_line: line(d.start || 0), message: ts.flattenDiagnosticMessageText(d.messageText, ' ')}));
process.stdout.write(JSON.stringify(output));
