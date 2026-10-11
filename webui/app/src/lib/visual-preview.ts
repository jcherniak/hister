export interface VisualPreviewEvent {
  type: 'progress' | 'complete' | 'error';
  completed?: number;
  total?: number;
  failed?: number;
  html?: string;
  captured_at?: string;
  error?: string;
}

export async function readVisualPreview(
  response: Response,
  receive: (event: VisualPreviewEvent) => void,
): Promise<void> {
  if (!response.ok || !response.body) throw new Error('Could not load the visual archive.');
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = '';
  let complete = false;
  const accept = (line: string) => {
    if (!line.trim()) return;
    const event = JSON.parse(line) as VisualPreviewEvent;
    if (event.type === 'error') throw new Error(event.error || 'Could not prepare the archive.');
    if (event.type === 'complete') complete = true;
    receive(event);
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      pending += decoder.decode(value, { stream: !done });
      if (pending.length > 192 * 1024 * 1024) throw new Error('Visual archive is too large.');
      let end: number;
      while ((end = pending.indexOf('\n')) !== -1) {
        accept(pending.slice(0, end));
        pending = pending.slice(end + 1);
      }
      if (done) break;
    }
    accept(pending);
    if (!complete) throw new Error('The visual archive response was interrupted.');
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
