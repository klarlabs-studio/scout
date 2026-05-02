<script setup lang="ts">
import { ref, nextTick, watch, onMounted, onBeforeUnmount } from "vue";
import type { ChatMessage } from "../types/agui";
import { renderMarkdown } from "../composables/useMarkdown";

const props = defineProps<{
  messages: ChatMessage[];
  isRunning: boolean;
  error: string | null;
}>();

const emit = defineEmits<{
  send: [text: string];
  cancel: [];
  clear: [];
}>();

const input = ref("");
const scrollEl = ref<HTMLElement | null>(null);
const inputEl = ref<HTMLInputElement | null>(null);

function submit() {
  const text = input.value.trim();
  if (!text || props.isRunning) return;
  input.value = "";
  emit("send", text);
}

// Cmd/Ctrl-K to focus input from anywhere; Esc to cancel a running run.
// Matches the convention from Cursor / Claude / ChatGPT so users don't have
// to relearn the chrome.
function onGlobalKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    e.preventDefault();
    inputEl.value?.focus();
    inputEl.value?.select();
    return;
  }
  if (e.key === "Escape" && props.isRunning) {
    e.preventDefault();
    emit("cancel");
  }
}

onMounted(() => window.addEventListener("keydown", onGlobalKey));
onBeforeUnmount(() => window.removeEventListener("keydown", onGlobalKey));

watch(
  () => props.messages.length,
  () =>
    nextTick(() =>
      scrollEl.value?.scrollTo({
        top: scrollEl.value.scrollHeight,
        behavior: "smooth",
      })
    )
);

// Also scroll on streaming content changes, not just on count changes,
// so live token deltas pull the view down as the assistant types.
watch(
  () => props.messages[props.messages.length - 1]?.content,
  () =>
    nextTick(() =>
      scrollEl.value?.scrollTo({
        top: scrollEl.value.scrollHeight,
        behavior: "auto",
      })
    )
);
</script>

<template>
  <div class="flex flex-col h-full bg-zinc-950">
    <!-- Header -->
    <div
      class="flex items-center justify-between px-5 py-3.5 border-b border-zinc-800/60"
    >
      <div class="flex items-center gap-2.5">
        <div
          class="w-7 h-7 rounded-lg bg-gradient-to-br from-blue-500 to-violet-600 flex items-center justify-center"
        >
          <svg
            class="w-4 h-4 text-white"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <circle cx="12" cy="12" r="10" />
            <path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
          </svg>
        </div>
        <span class="text-sm font-semibold text-zinc-100">Scout</span>
      </div>
      <button
        v-if="messages.length"
        class="text-[11px] text-zinc-500 hover:text-zinc-300 transition-colors px-2 py-1 rounded hover:bg-zinc-800/60"
        @click="emit('clear')"
      >
        Clear
      </button>
    </div>

    <!-- Messages -->
    <div ref="scrollEl" class="flex-1 overflow-y-auto px-4 py-4 space-y-3">
      <!-- Empty state -->
      <div
        v-if="!messages.length"
        class="flex flex-col items-center justify-center h-full gap-4 px-6"
      >
        <div
          class="w-12 h-12 rounded-2xl bg-zinc-800/80 flex items-center justify-center"
        >
          <svg
            class="w-6 h-6 text-zinc-500"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
          >
            <path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9 9a9 9 0 0 1-9-9m9 9c1.66 0 3-4.03 3-9s-1.34-9-3-9m0 18c-1.66 0-3-4.03-3-9s1.34-9 3-9m-9 9a9 9 0 0 1 9-9" />
          </svg>
        </div>
        <div class="text-center space-y-1.5">
          <p class="text-sm font-medium text-zinc-300">
            What should I do?
          </p>
          <p class="text-xs text-zinc-500 leading-relaxed max-w-[240px]">
            Tell me to navigate, click, fill forms, extract data, or take screenshots.
          </p>
        </div>
        <div class="flex flex-wrap gap-1.5 justify-center mt-1">
          <button
            v-for="hint in [
              'Go to github.com',
              'Find trending repos',
              'Take a screenshot',
            ]"
            :key="hint"
            class="text-[11px] text-zinc-400 bg-zinc-800/60 hover:bg-zinc-800 px-2.5 py-1 rounded-full transition-colors cursor-pointer"
            @click="emit('send', hint)"
          >
            {{ hint }}
          </button>
        </div>
      </div>

      <!-- Message list -->
      <div
        v-for="msg in messages"
        :key="msg.id"
        class="flex"
        :class="msg.role === 'user' ? 'justify-end' : 'justify-start'"
      >
        <div
          class="max-w-[85%] rounded-2xl px-3.5 py-2.5 text-[13px] leading-relaxed"
          :class="
            msg.role === 'user'
              ? 'bg-blue-600 text-white rounded-br-md'
              : 'bg-zinc-800/80 text-zinc-200 rounded-bl-md'
          "
        >
          <p
            v-if="msg.role === 'user'"
            class="whitespace-pre-wrap"
          >{{ msg.content }}</p>
          <div
            v-else
            class="prose-sm leading-relaxed space-y-0.5 [&_a]:text-blue-400 [&_a:hover]:underline [&_strong]:text-zinc-100"
            v-html="renderMarkdown(msg.content)"
          />
          <span
            v-if="msg.status === 'streaming'"
            class="inline-block w-1 h-3.5 ml-0.5 bg-zinc-400 animate-pulse rounded-sm align-middle"
          />
        </div>
      </div>

      <!-- Error -->
      <div
        v-if="error"
        class="rounded-xl bg-red-500/10 border border-red-500/20 px-3.5 py-2.5 text-[13px] text-red-400"
      >
        {{ error }}
      </div>
    </div>

    <!-- Input -->
    <div class="px-3 pb-3 pt-1">
      <form
        class="flex items-center gap-2 rounded-xl bg-zinc-900 border border-zinc-800/80 px-3 py-1.5 focus-within:border-zinc-700 transition-colors"
        @submit.prevent="submit"
      >
        <input
          ref="inputEl"
          v-model="input"
          :disabled="isRunning"
          class="flex-1 bg-transparent text-sm text-zinc-100 placeholder-zinc-500 outline-none disabled:opacity-50 py-1"
          placeholder="Ask Scout… (⌘K to focus, Esc to stop)"
        />
        <!-- Stop button (while running). Labelled so it's the most legible
             control on screen at the moment it matters most. -->
        <button
          v-if="isRunning"
          type="button"
          aria-label="Stop run (Esc)"
          title="Stop (Esc)"
          class="shrink-0 inline-flex items-center gap-1.5 rounded-lg bg-red-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-500 transition-colors"
          @click="emit('cancel')"
        >
          <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="currentColor">
            <rect x="6" y="6" width="12" height="12" rx="1" />
          </svg>
          <span>Stop</span>
        </button>
        <!-- Send button -->
        <button
          v-else
          type="submit"
          :disabled="!input.trim()"
          class="shrink-0 rounded-lg bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-500 disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
        >
          <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="currentColor">
            <path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z" />
          </svg>
        </button>
      </form>
    </div>
  </div>
</template>
