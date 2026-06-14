import { EditorView, basicSetup } from 'codemirror';
import { EditorState } from '@codemirror/state';
import { linter, lintGutter } from '@codemirror/lint';
import { parseLogstash } from './wasm-bridge.js';

const SAMPLE = `input {
  beats {
    port => 5044
  }
}

filter {
  mutate {
    add_tag => ["processed"]
  }
}

output {
  elasticsearch {
    hosts => ["http://localhost:9200"]
    index => "logstash-%{+YYYY.MM.dd}"
  }
}
`;

const logstashLinter = linter(async (view) => {
  const doc = view.state.doc.toString();
  if (!doc.trim()) return [];

  try {
    const result = await parseLogstash(doc);

    const diagnostics = (result.diagnostics || []).map(d => ({
      from: Math.max(0, d.from),
      to: Math.min(d.to, doc.length),
      severity: d.severity,
      message: d.message,
    }));

    if (!result.ok && result.farthest && !diagnostics.some(d => d.from === result.farthest.from)) {
      diagnostics.push({
        from: Math.max(0, result.farthest.from),
        to: Math.min(result.farthest.to, doc.length),
        severity: result.farthest.severity,
        message: result.farthest.message,
      });
    }

    return diagnostics;
  } catch (err) {
    console.error('Linter error:', err);
    return [];
  }
}, { delay: 300 });

export function createEditor(parent) {
  const view = new EditorView({
    state: EditorState.create({
      doc: SAMPLE,
      extensions: [
        basicSetup,
        lintGutter(),
        logstashLinter,
        EditorView.theme({
          '&': { height: '100%', backgroundColor: '#1e1e1e', color: '#d4d4d4' },
          '.cm-scroller': { overflow: 'auto' },
          '.cm-content': { caretColor: '#d4d4d4' },
          '&.cm-focused .cm-cursor': { borderLeftColor: '#d4d4d4' },
          '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
            backgroundColor: '#37373d',
          },
          '.cm-gutters': {
            backgroundColor: '#252526',
            color: '#858585',
            border: 'none',
            borderRight: '1px solid #3c3c3c',
          },
          '.cm-activeLineGutter': { backgroundColor: '#2a2d2e', color: '#c6c6c6' },
          '.cm-activeLine': { backgroundColor: '#2a2d2e40' },
          '.cm-foldPlaceholder': { backgroundColor: '#3c3c3c', color: '#d4d4d4', border: 'none' },
        }, { dark: true }),
      ],
    }),
    parent,
  });

  return {
    view,
    getContent() {
      return view.state.doc.toString();
    },
    setContent(text) {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: text },
      });
    },
  };
}
