// Carry existing display preferences across the project rename.
for (const setting of ['theme', 'layout', 'sidebar']) {
  const key = 'diflett.' + setting;
  const previous = localStorage.getItem('gitwatch.' + setting);
  if (previous !== null && localStorage.getItem(key) === null) {
    localStorage.setItem(key, previous);
  }
}
const $ = s => document.querySelector(s);
const themeButton = $('#theme');
function setTheme(dark) {
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  themeButton.setAttribute('aria-pressed', String(dark));
}
setTheme(localStorage.getItem('diflett.theme') !== 'light');
themeButton.onclick = () => {
  setTheme(document.documentElement.dataset.theme !== 'dark');
  localStorage.setItem('diflett.theme', document.documentElement.dataset.theme);
};

const groups = ['Unstaged', 'Staged', 'Untracked'];
let projects, selected, stream, snapshot, group = 'Unstaged';
const folded = new Set();
const fileContexts = new Map();
function node(tag, text, cls) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (cls) el.className = cls;
  return el;
}
function status(text, live = false) {
  $('#connection').textContent = text;
  $('#connection').classList.toggle('live', live);
}
let layout = localStorage.getItem('diflett.layout') === 'dual' ? 'dual' : 'single';

const sidebarToggle = $('#sidebar-toggle');
function showSidebar(visible) {
  $('#repositories').hidden = !visible;
  $('main').classList.toggle('wide', !visible);
  sidebarToggle.setAttribute('aria-expanded', String(visible));
  sidebarToggle.textContent = visible ? 'Hide repositories' : 'Show repositories';
}
showSidebar(localStorage.getItem('diflett.sidebar') !== 'hidden');
sidebarToggle.onclick = () => {
  showSidebar($('#repositories').hidden);
  localStorage.setItem('diflett.sidebar', $('#repositories').hidden ? 'hidden' : 'visible');
};

function paint() {
  $('#layout').hidden = group === 'Untracked';
  for (const b of $('#layout').children) b.setAttribute('aria-pressed', b.dataset.mode === layout);
  const container = $('#diff');
  container.replaceChildren();
  for (const btn of $('.tabs').children) btn.setAttribute('aria-pressed', btn.textContent === group);
  if (!snapshot || snapshot.Error) {
    container.append(node('p', snapshot?.Error || 'Reading repository…', 'empty'));
    return;
  }
  const data = snapshot[group];
  if (!data?.length) {
    container.append(node('p', group === 'Untracked' ? 'No untracked files.' : 'No ' + group.toLowerCase() + ' changes.', 'empty'));
  } else if (group === 'Untracked') {
    const list = node('ul', undefined, 'files');
    for (const path of data) list.append(node('li', path));
    container.append(list);
  } else {
    const visibleFiles = new Set();
    for (const block of data.replace(/\n$/, '').split(/\n(?=diff --git )/)) {
      const [header, ...lines] = block.split('\n');
      const key = JSON.stringify([selected, group, header]);
      const card = node('details');
      card.open = !folded.has(key);
      const paths = patchPaths(lines);
      const title = paths.after || paths.before || header.replace(/^diff --git /, '');
      card.append(node('summary', title));
      card.ontoggle = () => { if (card.isConnected) card.open ? folded.delete(key) : folded.add(key); };
      container.append(card);
      visibleFiles.add(key);
      let context = fileContexts.get(key);
      if (!context || context.patch !== block) {
        context = {patch:block, project:selected, gaps:new Map()};
        fileContexts.set(key, context);
      }
      card.append(renderDiff(lines, paths, layout, context));

    }
    for (const key of fileContexts.keys()) if (!visibleFiles.has(key)) fileContexts.delete(key);
  }
}
function select(id) {
  stream?.close(); selected = id; snapshot = null;
  $('#project').textContent = projects[id].Name; $('#path').textContent = projects[id].Root;
  $('#branch').textContent = ''; status('Connecting');
  [...$('nav').children].forEach((b,i) => b.setAttribute('aria-pressed', i === id));
  paint();
  const source = new EventSource('/api/events/' + id); stream = source;
  source.onmessage = e => {
    if (source !== stream) return;
    snapshot = JSON.parse(e.data); $('#branch').textContent = snapshot.Branch;
    status(snapshot.Error ? 'Repository unavailable' : 'Live', !snapshot.Error); paint();
  };
  source.onerror = () => { if (source === stream) status('Reconnecting…'); };
  source.onopen = () => { if (source === stream) status('Connected'); };
}
function renderProjects() {
  $('nav').replaceChildren();
  projects.forEach((p,i) => {
    const duplicate = projects.some((q,j) => j !== i && q.Name === p.Name);
    const b = node('button', duplicate ? p.Root : p.Name); b.title = p.Root; b.onclick = () => select(i); $('nav').append(b);
  });
}
const dialog = $('#add-dialog');
$('#open-add').onclick = () => {
  $('#add-error').textContent = ''; $('#add-form').reset(); dialog.showModal();
};
$('#cancel').onclick = () => dialog.close();
$('#add-form').onsubmit = async event => {
  event.preventDefault();
  $('#submit-add').disabled = true;
  $('#add-error').textContent = '';
  try {
    const response = await fetch('/api/projects', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({path: $('#repo-path').value})
    });
    if (!response.ok) throw new Error((await response.text()).trim());
    const id = await response.json();
    projects = await (await fetch('/api/projects')).json();
    renderProjects(); select(id); dialog.close();
  } catch (err) {
    $('#add-error').textContent = err.message;
  } finally {
    $('#submit-add').disabled = false;
  }
};
async function start() {
  projects = await (await fetch('/api/projects')).json();
  for (const name of groups) {
    const b = node('button', name); b.onclick = () => { group = name; paint(); }; $('.tabs').append(b);
  }
  renderProjects();
  for (const b of $('#layout').children) b.onclick = () => {
    layout = b.dataset.mode; localStorage.setItem('diflett.layout', layout); paint();
  };
  select(0);
}
start().catch(() => status('Unable to load repositories'));
