// Lightweight markdown-to-HTML renderer. Handles the subset LLMs typically produce:
// **bold**, *italic*, `code`, ```code blocks```, [links](url), - lists, headings

function escape(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// Only http(s) and mailto links are allowed. javascript:, data:, vbscript:,
// file: etc. become "#" so an LLM-emitted `[x](javascript:...)` cannot
// execute code via v-html injection.
function safeHref(url: string): string {
  const trimmed = url.trim();
  if (/^(https?:|mailto:)/i.test(trimmed)) return trimmed.replace(/"/g, "%22");
  if (/^[/#]/.test(trimmed)) return trimmed.replace(/"/g, "%22");
  return "#";
}

export function renderMarkdown(md: string): string {
  if (!md) return "";

  const lines = md.split("\n");
  const out: string[] = [];
  let inCode = false;

  for (const line of lines) {
    // Fenced code blocks
    if (line.startsWith("```")) {
      if (inCode) {
        out.push("</code></pre>");
        inCode = false;
      } else {
        out.push(
          '<pre class="bg-zinc-900 rounded-lg px-3 py-2 my-1 text-xs overflow-x-auto"><code>'
        );
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      out.push(escape(line));
      continue;
    }

    let html = escape(line);

    // Headings
    const headingMatch = html.match(/^(#{1,3})\s+(.+)$/);
    if (headingMatch) {
      const level = headingMatch[1].length;
      const sizes = ["text-base font-semibold", "text-sm font-semibold", "text-sm font-medium"];
      html = `<div class="${sizes[level - 1]} mt-1">${headingMatch[2]}</div>`;
      out.push(html);
      continue;
    }

    // List items
    if (html.match(/^[-*]\s+/)) {
      html = html.replace(
        /^[-*]\s+/,
        '<span class="text-zinc-500 mr-1">&bull;</span>'
      );
    }

    // Inline formatting
    html = html
      .replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")
      .replace(/\*(.+?)\*/g, "<em>$1</em>")
      .replace(
        /`([^`]+)`/g,
        '<code class="bg-zinc-900 px-1 py-0.5 rounded text-[11px] text-blue-300">$1</code>'
      )
      .replace(
        /\[([^\]]+)\]\(([^)]+)\)/g,
        (_m, label: string, url: string) =>
          `<a href="${safeHref(url)}" class="text-blue-400 hover:underline" target="_blank" rel="noopener noreferrer">${label}</a>`
      );

    out.push(html || "<br/>");
  }

  if (inCode) out.push("</code></pre>");
  return out.join("\n");
}
