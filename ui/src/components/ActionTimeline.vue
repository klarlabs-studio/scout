<script setup lang="ts">
import type { ToolAction } from "../types/agui";

defineProps<{
  actions: ToolAction[];
  activeTool: string;
}>();

function formatTime(ts: number): string {
  return new Date(ts).toLocaleTimeString("en", {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function truncateArgs(args: string, max = 50): string {
  if (!args || args === "{}") return "";
  try {
    const parsed = JSON.parse(args);
    const short = JSON.stringify(parsed);
    return short.length > max ? short.slice(0, max) + "\u2026" : short;
  } catch {
    return args.length > max ? args.slice(0, max) + "\u2026" : args;
  }
}

const toolIcons: Record<string, string> = {
  navigate: "\u{1F310}",
  observe: "\u{1F441}",
  click: "\u{1F446}",
  type: "\u{2328}",
  screenshot: "\u{1F4F8}",
  extract: "\u{1F4CB}",
  scroll_to: "\u{2195}",
  fill_form_semantic: "\u{1F4DD}",
  dismiss_cookies: "\u{1F36A}",
  markdown: "\u{1F4C4}",
};

function icon(name: string): string {
  return toolIcons[name] ?? "\u{2699}";
}
</script>

<template>
  <div class="h-full overflow-y-auto bg-zinc-950">
    <div class="px-4 py-2.5">
      <div class="text-[10px] font-medium text-zinc-600 uppercase tracking-widest mb-2">
        Activity
      </div>

      <!-- Empty -->
      <div
        v-if="!actions.length && !activeTool"
        class="text-center text-zinc-700 text-xs py-6"
      >
        Tool calls will stream here
      </div>

      <div class="space-y-0.5">
        <!-- Active indicator -->
        <div
          v-if="activeTool"
          class="flex items-center gap-2 px-2.5 py-1.5 rounded-lg bg-blue-500/8 border border-blue-500/15 mb-1"
        >
          <span class="relative flex h-2 w-2 shrink-0">
            <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-60" />
            <span class="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
          </span>
          <span class="text-[12px] text-blue-300 font-mono">{{ activeTool }}</span>
        </div>

        <!-- Completed actions -->
        <div
          v-for="action in actions"
          :key="action.id"
          class="flex items-center gap-2 px-2.5 py-1 rounded-lg hover:bg-zinc-900/60 group transition-colors"
        >
          <span class="text-xs shrink-0 opacity-70 group-hover:opacity-100 transition-opacity">{{ icon(action.name) }}</span>
          <span class="text-[12px] font-mono text-zinc-400 group-hover:text-zinc-300 transition-colors">{{ action.name }}</span>
          <span
            v-if="truncateArgs(action.args)"
            class="text-[11px] text-zinc-600 truncate"
          >
            {{ truncateArgs(action.args) }}
          </span>
          <span class="flex-1" />
          <span
            class="w-1.5 h-1.5 rounded-full shrink-0"
            :class="{
              'bg-emerald-500': action.status === 'done',
              'bg-red-500': action.status === 'error',
              'bg-amber-400': action.status === 'unknown',
              'bg-blue-500 animate-pulse': action.status === 'running',
            }"
            :title="action.status === 'unknown' ? 'Outcome unknown · inspect state before retrying' : action.status"
          />
          <span class="text-[10px] text-zinc-700 tabular-nums shrink-0">
            {{ formatTime(action.timestamp) }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>
