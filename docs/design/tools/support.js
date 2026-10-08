// Minimal stand-in for the Design canvas runtime, only for static PNG renders.
window.DCLogic = class { constructor(props){ this.props = props || {}; this.state = {}; } setState(s){ Object.assign(this.state, s); } };
document.addEventListener('DOMContentLoaded', () => {
  const sc = document.querySelector('script[data-dc-script]');
  const meta = JSON.parse(sc.getAttribute('data-props') || '{}');
  const props = {};
  for (const [k, v] of Object.entries(meta)) if (!k.startsWith('$') && v && 'default' in v) props[k] = v.default;
  const Component = new Function('DCLogic', sc.textContent + '\nreturn Component;')(window.DCLogic);
  const vals = new Component(props).renderVals();
  const look = (path, scope) => path.trim().split('.').reduce((o, k) => (o == null ? undefined : o[k]), scope);
  const sub = (s, scope) => s.replace(/\{\{([^}]+)\}\}/g, (_, p) => { const v = look(p, scope); return typeof v === 'function' || v === undefined ? '' : String(v); });
  const render = (node, scope) => {
    for (const child of [...node.childNodes]) {
      if (child.nodeType === 3) { child.textContent = sub(child.textContent, scope); continue; }
      if (child.nodeType !== 1) continue;
      const tag = child.tagName.toLowerCase();
      if (tag === 'sc-for') {
        const list = look(child.getAttribute('list').replace(/[{}]/g, ''), scope) || [];
        const as = child.getAttribute('as');
        const frag = document.createDocumentFragment();
        for (const item of list) {
          const tpl = document.createElement('div');
          tpl.innerHTML = child.innerHTML;
          render(tpl, Object.assign({}, scope, { [as]: item }));
          frag.append(...tpl.childNodes);
        }
        child.replaceWith(frag); continue;
      }
      if (tag === 'sc-if') {
        const v = look(child.getAttribute('value').replace(/[{}]/g, ''), scope);
        if (!v) { child.remove(); continue; }
        render(child, scope); child.replaceWith(...child.childNodes); continue;
      }
      for (const a of [...child.attributes]) {
        if (/^on/i.test(a.name)) { child.removeAttribute(a.name); continue; }
        if (a.value.includes('{{')) child.setAttribute(a.name, sub(a.value, scope));
      }
      render(child, scope);
    }
  };
  const root = document.querySelector('x-dc');
  root.querySelectorAll('helmet').forEach((h) => { document.head.append(...h.childNodes); h.remove(); });
  render(root, vals);
  root.replaceWith(...root.childNodes);
  document.body.dataset.ready = '1';
});
