// Git quotes unusual path bytes as C-style escapes, including octal UTF-8.
function patchPaths(lines) {
  const paths = {};
  for (const line of lines) {
    if (line.startsWith('@@')) break;
    if (!line.startsWith('--- ') && !line.startsWith('+++ ')) continue;
    let path = line.slice(4).replace(/\t$/, '');
    if (path.startsWith('"')) {
      path = path.slice(1, -1).replace(/(?:\\[0-7]{3})+|\\(.)/g, (match, escaped) => {
        if (escaped) return ({n:'\n', t:'\t', r:'\r'})[escaped] ?? escaped;
        return new TextDecoder().decode(Uint8Array.from(match.split('\\').slice(1), n => parseInt(n, 8)));
      });
    }
    if (path !== '/dev/null') paths[line[0] === '+' ? 'after' : 'before'] = path.replace(/^[ab]\//, '');
  }
  return paths;
}

function sourceLanguage(path = '') {
  const name = path.split('/').pop().toLowerCase();
  const special = {dockerfile:'dockerfile', makefile:'makefile', gemfile:'ruby', '.bashrc':'bash'};
  const ext = name.split('.').pop();
  const aliases = {jsx:'javascript', tsx:'typescript', h:'cpp', hpp:'cpp', sh:'bash', zsh:'bash', mjs:'javascript', cjs:'javascript', svg:'xml', htm:'xml', yml:'yaml'};
  const lang = special[name] || aliases[ext] || ext;
  return hljs.getLanguage(lang) ? lang : null;
}

// Highlight each visible hunk as a block so multiline tokens keep their color.
// Only copy text and token classes from the highlighter into the displayed diff.
function highlightSide(entries, language, side) {
  if (!entries.length || !language) return;
  const source = entries.map(entry => entry.text).join('\n');
  const template = document.createElement('template');
  template.innerHTML = hljs.highlight(source, {language, ignoreIllegals:true}).value;
  let index = 0;
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT);
  while (walker.nextNode()) {
    const text = walker.currentNode;
    const classes = [];
    for (let el = text.parentElement; el; el = el.parentElement) classes.push(...el.classList);
    const parts = text.data.split('\n');
    parts.forEach((part, offset) => {
      if (offset) index++;
      if (!part) return;
      const entry = entries[index];
      entry[side] ??= document.createDocumentFragment();
      entry[side].append(classes.length ? node('span', part, classes.join(' ')) : document.createTextNode(part));
    });
  }
}

function renderDiff(lines, paths, layout, context) {
  const dual = layout === 'dual';
  const view = node('div', undefined, dual ? 'split' : 'code');
  const body = node('div', undefined, dual ? '' : 'lines');
  view.append(body);
  if (dual) {
    const labels = node('div', undefined, 'pair labels');
    labels.append(node('div', 'Before'), node('div', 'After')); body.append(labels);
  }
  let oldNumber, newNumber, entries = [];
  const oldLanguage = sourceLanguage(paths.before);
  const newLanguage = sourceLanguage(paths.after);
  function cell(entry, side) {
    const el = node('div', undefined, 'source-cell');
    const number = entry?.[side === 'before' ? 'old' : 'new'];
    const content = node('span', undefined, 'source-text ' + (entry?.kind || ''));
    if (entry) content.append(entry[side] || document.createTextNode(entry.text));
    if (entry?.note) content.append(node('span', entry.note, 'newline-note meta'));
    el.append(node('span', String(number ?? ''), 'gutter'), content);
    return el;
  }
  function pair(left, right, target = body) {
    const row = node('div', undefined, 'pair');
    row.append(cell(left, 'before'), cell(right, 'after')); target.append(row);
  }
  function unified(entry, target) {
    const row = cell(entry, entry.kind === 'del' ? 'before' : 'after');
    row.classList.add('unified');
    row.lastChild.prepend(document.createTextNode(entry.prefix));
    target.append(row);
  }
  const index = lines.find(line => line.startsWith('index '));
  const blob = index?.match(/^index ([0-9a-f]+)\.\./)?.[1];
  function gap(oldStart, newStart, length, leading = false) {
    if (!blob || /^0+$/.test(blob) || length <= 0) return;
    const key = oldStart;
    if (!context.gaps.has(key)) context.gaps.set(key, {shown:[], more:true});
    const state = context.gaps.get(key);
    const wrapper = node('div');
    body.append(wrapper);

    async function load(start, count) {
      const response = await fetch('/api/context/' + context.project + '/' + blob + '?start=' + start + '&count=' + count);
      if (!response.ok) throw new Error(await response.text());
      return response.json();
    }

    function draw() {
      wrapper.replaceChildren();
      const startOffset = leading ? length - state.shown.length : 0;
      const rows = state.shown.map((text, i) => ({text, old:oldStart + startOffset + i,
        new:newStart + startOffset + i, kind:'', prefix:' '}));
      highlightSide(rows, newLanguage || oldLanguage, 'after');
      if (dual) highlightSide(rows, oldLanguage, 'before');
      for (const row of rows) dual ? pair(row, row, wrapper) : unified(row, wrapper);
      const remaining = length - rows.length;
      if (!state.more || remaining <= 0) return;
      const count = Math.min(10, remaining, state.next?.lines.length ?? 10);
      const label = 'View ' + count + ' hidden ' + (count === 1 ? 'line' : 'lines');
      const button = node('button', label, 'context-divider');
      button.type = 'button';
      if (leading) wrapper.prepend(button); else wrapper.append(button);
      button.onclick = async () => {
        button.disabled = true;
        try {
          const start = oldStart + (leading ? remaining - count : rows.length);
          const page = state.next || await load(start, count);
          if (!wrapper.isConnected) return;
          if (leading) state.shown.unshift(...page.lines); else state.shown.push(...page.lines);
          state.next = null;
          state.more = Number.isFinite(length) ? page.lines.length === count : page.more;
          if (length === Infinity && page.more) {
            // Preview the next batch to label the final partial page accurately.
            await peekTail();
          } else draw();
        } catch {
          button.disabled = false; button.textContent = 'Retry loading hidden lines';
        }
      };
    }
    async function peekTail() {
      try {
        state.next = await load(oldStart + state.shown.length, 10);
        state.more = state.next.lines.length > 0;
        draw();
      } catch {
        // Keep the already expanded rows and allow a manual retry.
        draw();
      }
    }
    if (length === Infinity && state.more && !state.next) peekTail(); else draw();
  }
  function flush() {
    if (!entries.length) return;
    highlightSide(entries.filter(e => e.old !== undefined), oldLanguage, 'before');
    highlightSide(entries.filter(e => e.new !== undefined), newLanguage, 'after');
    if (dual) {
      let removed = [], added = [];
      function changes() {
        for (let i = 0; i < Math.max(removed.length, added.length); i++) pair(removed[i], added[i]);
        removed = []; added = [];
      }
      for (const entry of entries) {
        if (entry.kind === 'del') {
          if (added.length) changes();
          removed.push(entry);
        } else if (entry.kind === 'add') added.push(entry);
        else { changes(); pair(entry, entry); }
      }
      changes();
    } else {
      for (const entry of entries) unified(entry, body);
    }
    entries = [];
  }
  let seenHunk = false;
  let trailingContext = 0;
  for (const line of lines) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (hunk) {
      flush();
      const oldStart = Number(hunk[1]), newStart = Number(hunk[2]);
      const previousOld = oldNumber ?? 1, previousNew = newNumber ?? 1;
      const omitted = oldStart - previousOld;
      if (omitted === newStart - previousNew) gap(previousOld, previousNew, omitted, !seenHunk);
      oldNumber = oldStart; newNumber = newStart; seenHunk = true; trailingContext = 0;
      body.append(node('div', line, 'line hunk'));
    } else if (oldNumber !== undefined && /^[ +\-]/.test(line)) {
      const prefix = line[0];
      trailingContext = prefix === ' ' ? trailingContext + 1 : 0;
      entries.push({text:line.slice(1), prefix, kind:prefix === '+' ? 'add' : prefix === '-' ? 'del' : '',
        old:prefix === '+' ? undefined : oldNumber++, new:prefix === '-' ? undefined : newNumber++});
    } else if (entries.length && line.startsWith('\\')) {
      entries[entries.length - 1].note = line;
    } else {
      flush(); oldNumber = undefined;
      body.append(node('div', line, 'line meta'));
    }
  }
  flush();
  if (oldNumber !== undefined && trailingContext >= 3) gap(oldNumber, newNumber, Infinity);
  return view;
}
