// Voice input for a text field, using the browser's speech recognition
// (Siri's engine on iOS Safari). Shared by the chat page and the screen page.
//
// attachVoice(button, input): tap the button to dictate into input, tap again
// to stop; returns { stop, cancel }. Long-press switches between Vietnamese
// and English. The button is hidden when the browser has no speech recognition.
(function () {
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  const LANGS = [
    { code: 'vi-VN', label: 'VI' },
    { code: 'en-US', label: 'EN' },
  ];
  const LONG_PRESS_MS = 600;

  function loadLang() {
    try { return Math.max(0, LANGS.findIndex(l => l.code === localStorage.getItem('voice-lang'))); }
    catch (e) { return 0; }
  }

  window.attachVoice = function (button, input) {
    if (!Recognition) { button.hidden = true; return; }

    let lang = loadLang();
    let rec = null;
    let base = '';      // text already in the field when dictation started
    let finalText = ''; // recognised phrases that will not change any more

    function label() {
      button.textContent = (rec ? '⏺ ' : '🎤 ') + LANGS[lang].label;
      button.classList.toggle('listening', !!rec);
    }

    function show(interim) {
      const spoken = (finalText + interim).trim();
      input.value = base + (base && spoken && !base.endsWith(' ') ? ' ' : '') + spoken;
      // Let the page react (auto-grow textarea, etc.) as if the user typed.
      input.dispatchEvent(new Event('input'));
    }

    function stop() {
      if (rec) rec.stop();
    }

    // Drop dictation without touching the field: used when the message is
    // sent, so a late result cannot write the old text back.
    function cancel() {
      if (!rec) return;
      rec.onresult = rec.onend = null;
      rec.abort();
      rec = null;
      label();
    }

    function start() {
      base = input.value;
      finalText = '';
      rec = new Recognition();
      rec.lang = LANGS[lang].code;
      rec.interimResults = true;
      rec.continuous = true;
      rec.onresult = (e) => {
        let interim = '';
        for (let i = e.resultIndex; i < e.results.length; i++) {
          const r = e.results[i];
          if (r.isFinal) finalText += r[0].transcript + ' ';
          else interim += r[0].transcript;
        }
        show(interim);
      };
      rec.onerror = (e) => {
        if (e.error === 'not-allowed' || e.error === 'service-not-allowed') {
          alert('Microphone or speech recognition is blocked. On iPhone, allow the microphone for this site and turn on Siri (Settings → Siri) so dictation can run.');
        } else if (e.error !== 'no-speech' && e.error !== 'aborted') {
          console.warn('speech error', e.error);
        }
      };
      rec.onend = () => {
        show('');
        rec = null;
        label();
      };
      try {
        rec.start();
      } catch (e) {
        rec = null;
      }
      label();
    }

    // Tap toggles dictation; a long press switches language instead.
    let pressTimer = null;
    let longPressed = false;
    button.addEventListener('pointerdown', () => {
      longPressed = false;
      pressTimer = setTimeout(() => {
        longPressed = true;
        stop();
        lang = (lang + 1) % LANGS.length;
        try { localStorage.setItem('voice-lang', LANGS[lang].code); } catch (e) {}
        label();
      }, LONG_PRESS_MS);
    });
    const cancelPress = () => clearTimeout(pressTimer);
    button.addEventListener('pointerup', cancelPress);
    button.addEventListener('pointerleave', cancelPress);
    button.addEventListener('click', (e) => {
      e.preventDefault();
      if (longPressed) return;
      if (rec) stop(); else start();
    });
    button.addEventListener('contextmenu', (e) => e.preventDefault());

    label();
    return { stop, cancel };
  };
})();
