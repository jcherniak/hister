<script lang="ts">
  import { saveVisualSettings, type RulesData } from '#lib/rules.js';
  import { Button } from '@hister/components/ui/button';
  import * as Card from '@hister/components/ui/card';

  let { rules, onSaved }: { rules: RulesData; onSaved: () => Promise<void> } = $props();
  let visualMode: 'disabled' | 'opt-in' | 'opt-out' = $state('opt-out');
  let saving = $state(false);
  let message = $state('');
  let isError = $state(false);
  $effect(() => {
    visualMode = rules.visual_mode ?? 'opt-out';
  });

  async function saveVisual(settings: Parameters<typeof saveVisualSettings>[0]) {
    if (saving) return;
    saving = true;
    message = '';
    try {
      await saveVisualSettings(settings);
      await onSaved();
      message = 'Visual archive settings saved';
      isError = false;
    } catch (error) {
      message = String(error);
      isError = true;
    } finally {
      saving = false;
    }
  }
</script>

<Card.Root id="visual-archive" class="scroll-mt-6 lg:col-span-2">
  <Card.Header color="hister-teal">
    <div class="flex flex-col gap-1">
      <Card.Title class="font-space text-xl font-extrabold text-white">Visual archive</Card.Title>
      <Card.Description class="text-sm text-white/80"
        >Capture page styles and shared resources without changing search.</Card.Description
      >
    </div>
  </Card.Header>
  <Card.Content class="space-y-4 p-4 md:p-5">
    <p class="text-sm">
      Disabled stops visual capture and display for this installation. Opt-in captures only enabled
      sites. Opt-out captures all sites except disabled sites. Indexing exclusions still apply.
      Settings do not delete existing captures.
    </p>
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void saveVisual({ visual_mode: visualMode });
      }}
      class="flex flex-wrap items-end gap-3"
    >
      <label class="flex flex-col gap-1 text-sm font-bold" for="visual-mode"
        >Installation mode
        <select
          id="visual-mode"
          bind:value={visualMode}
          disabled={!rules.visual_can_configure_install || saving}
          class="border-brutal-border bg-card-surface border-2 p-2"
        >
          <option value="disabled">Disabled</option>
          <option value="opt-in">Opt-in</option>
          <option value="opt-out">Opt-out</option>
        </select>
      </label>
      {#if rules.visual_can_configure_install}<Button type="submit" disabled={saving}
          >Save mode</Button
        >{/if}
    </form>
    {#if !rules.visual_can_configure_install}<p class="text-sm">
        Only an administrator can change the mode. Installation configuration can enforce disabled
        capture.
      </p>{/if}
    <p class="text-sm">
      Opt sites in or out with the Visual archive option on indexing rules. A rule can also apply
      only to the visual archive without changing indexing.
    </p>
    {#if message}<p role="status" class:text-red-600={isError}>{message}</p>{/if}
  </Card.Content>
</Card.Root>
