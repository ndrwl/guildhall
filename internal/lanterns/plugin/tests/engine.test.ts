// Runs under the installed Claude Code function-hook engine; no model calls.
import { test, expect, mock } from 'claude-code/testing';

test('real engine defaults Calm on, preserves assistant content and paints S2', async ($, on) => {
  mock.env(on, { CLAUDE_CODE_ENABLE_FUNCTION_HOOKS: '1' });
  const clock = mock.clock(on);
  const commands: string[] = [];
  let saved = '', preference = '';
  // The session's calm.json beside the plugin names the copy's preference, which has not been saved yet.
  const calm = (path: string) => path.endsWith('/calm.json');
  on('fs.exists', async (_$, e) => ({ value: calm(e.path) }));
  on('fs.read', async (_$, e) => ({ value: calm(e.path) ? JSON.stringify({ preference: '/copy/director/lanterns-calm' }) : '' }));
  on('fs.write', async (_$, e) => { saved = e.text; preference = e.path; return { value: undefined }; });
  on('config.list', async () => ({ value: [] }));
  on('command.register', async (_$, e) => { commands.push(e.name); return { value: { command: e.name } }; });
  on('ui.toast', async () => ({ value: undefined }));
  on('ui.invalidate', async () => ({ value: undefined }));
  on('ui.render', async () => ({ type: 'Text', props: {}, children: ['native content'] }));
  const blits: string[] = [];
  on('ui.blit', async (_$, e) => { blits.push(e.cells); return { value: {} }; });
  on('session.start', async (_$, e) => ({ cwd: e.cwd }));
  await $.session.start({ cwd: '/work', surface: 'terminal', isInteractive: true });
  expect(commands).toEqual(['calm']);
  const frame = await $.ui.render({ component: 'AssistantMessage', surface: 'terminal', requestId: 'reply', props: { text: '🟡 choose' } });
  expect(frame.type).toBe('Box');
  expect(frame.props.borderStyle).toBe('round');
  expect(frame.props.borderColor).toBe('yellow');
  expect(saved).toBe('');
  // A reply in the Director's layout is drawn section by section inside the same frame.
  const reply = [
    '🟡 Guildmaster, one thing needs you.', '',
    '**Needs you**', '- d96 · approve the plan · accept-1', '',
    '**What I did**', 'Raised d95 to p0 as you asked.', '',
    '```text', 'd93 · merged · #228', '7 unchanged · /clerk:guild', 'as of #5812', '```',
  ].join('\n');
  const layout = await $.ui.render({ component: 'AssistantMessage', surface: 'terminal', requestId: 'layout', viewport: { columns: 60, rows: 24 }, props: { text: reply } });
  expect(layout.type).toBe('Box');
  expect(layout.props.borderStyle).toBe('round');
  expect(layout.props.borderColor).toBe('yellow');
  const sections = [layout.children ?? layout.props.children].flat();
  expect(sections.length).toBe(6);
  expect(JSON.stringify(sections)).toContain('routine');
  expect(JSON.stringify(sections)).toContain('as of #5812');
  // The marker line, Needs you and What I did each come back from the engine's own drawing.
  expect(sections.filter(s => JSON.stringify(s).includes('native content')).length).toBe(3);
  const spinner = await $.ui.render({ component: 'Spinner', surface: 'terminal', requestId: 'work', viewport: { columns: 30, rows: 24 }, props: { word: 'Working', message: null, mode: 'requesting' } });
  expect(spinner.type).toBe('Box');
  await clock.advance(880);
  expect(blits.length).toBe(4);
  await $.command.run({ command: 'calm', args: '', cwd: '/work' });
  expect(saved).toBe('off\n');
  expect(preference).toBe('/copy/director/lanterns-calm');
  await clock.advance(880);
  expect(blits.length).toBe(4);
  const native = await $.ui.render({ component: 'Spinner', surface: 'terminal', requestId: 'work', props: {} });
  expect(native.type).toBe('Text');
});
