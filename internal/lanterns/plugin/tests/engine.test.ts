// Runs under the installed Claude Code function-hook engine; no model calls.
import { test, expect, mock } from 'claude-code/testing';

test('real engine registers Calm, preserves assistant content and paints N8', async ($, on) => {
  mock.env(on, { CLAUDE_CODE_ENABLE_FUNCTION_HOOKS: '1', GUILDHALL_COPY: '/copy' });
  const clock = mock.clock(on);
  const commands: string[] = [];
  let saved = '', preference = '';
  on('fs.exists', async () => ({ value: false }));
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
  await $.command.run({ command: 'calm', args: '', cwd: '/work' });
  expect(saved).toBe('on\n');
  expect(preference).toBe('/copy/director/lanterns-calm');
  const spinner = await $.ui.render({ component: 'Spinner', surface: 'terminal', requestId: 'work', viewport: { columns: 30, rows: 24 }, props: { word: 'Working', message: null, mode: 'requesting' } });
  expect(spinner.type).toBe('Box');
  await clock.advance(880);
  expect(blits.length).toBe(4);
});
