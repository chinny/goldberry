// Goldberry progressive enhancement. Every page works without this file;
// it only adds the PIN pad, comment chips and live button labels.
document.documentElement.classList.add("js");

document.addEventListener("DOMContentLoaded", () => {
  // PIN pad: the real field is a password input; the pad types into it.
  for (const form of document.querySelectorAll("form[data-pin-form]")) {
    const input = form.querySelector("input[name=secret]");
    const dots = form.querySelectorAll(".pin-dots span");
    const len = Number(form.dataset.pinLength) || 4;
    const render = () => {
      dots.forEach((d, i) => d.classList.toggle("on", i < input.value.length));
      form.querySelector("[role=status]")?.setAttribute("aria-label", `${input.value.length} of ${len} digits entered`);
    };
    form.addEventListener("click", (e) => {
      const key = e.target.closest("button[data-digit], button[data-back]");
      if (!key) return;
      e.preventDefault();
      if (key.dataset.back !== undefined) input.value = input.value.slice(0, -1);
      else if (input.value.length < 6) input.value += key.dataset.digit;
      render();
      if (input.value.length === len) form.requestSubmit();
    });
    document.addEventListener("keydown", (e) => {
      if (document.activeElement && document.activeElement !== document.body && document.activeElement !== input) return;
      if (/^[0-9]$/.test(e.key)) form.querySelector(`button[data-digit="${e.key}"]`)?.click();
      else if (e.key === "Backspace") form.querySelector("button[data-back]")?.click();
    });
    render();
  }

  // Comment chips prefill the comment field (plan §7.1).
  for (const chip of document.querySelectorAll("button[data-fill]")) {
    chip.addEventListener("click", () => {
      const target = document.getElementById(chip.dataset.fill);
      if (!target) return;
      target.value = chip.dataset.text;
      for (const c of chip.parentElement.querySelectorAll("button[data-fill]")) c.setAttribute("aria-pressed", String(c === chip));
      target.focus();
    });
  }

  // "Add $5.00 for Ava" style submit labels.
  for (const form of document.querySelectorAll("form[data-live-label]")) {
    const btn = form.querySelector("[data-label]");
    const amount = form.querySelector("input[name=amount]");
    const symbol = form.dataset.symbol || "";
    const update = () => {
      const remove = form.querySelector("input[name=direction]:checked")?.value === "remove";
      const verb = remove ? "Remove" : "Add";
      const v = amount.value.trim();
      btn.textContent = v ? `${verb} ${symbol}${v} ${remove ? "from" : "for"} ${form.dataset.kid}` : `${verb} money`;
      btn.classList.toggle("btn-danger", remove);
      btn.classList.toggle("btn-primary", !remove);
    };
    form.addEventListener("input", update);
    form.addEventListener("change", update);
    update();
  }
});

// The gauntlet (plan §5.4): a 10 s countdown with escalating copy, then a
// 3 s press-and-hold. The server enforces the same 13 s, so skipping this
// script gains nothing.
document.addEventListener("DOMContentLoaded", () => {
  const form = document.querySelector("form[data-gauntlet]");
  if (!form) return;
  const btn = form.querySelector("button[data-hold]");
  const fill = btn.querySelector(".hold-fill");
  const label = btn.querySelector(".hold-label");
  const count = form.querySelector("[data-count]");
  const copy = form.querySelector("[data-count-copy]");
  const lines = [
    [7, "Are you sure? Take a breath. The button wakes up in a few seconds."],
    [4, "Past-you set this for a reason."],
    [1, "Okay, okay. Almost there."],
  ];
  let left = 10;
  btn.disabled = true;
  label.textContent = "Wait 10…";
  const tick = setInterval(() => {
    left -= 1;
    count.textContent = String(Math.max(left, 0));
    const line = lines.find(([at]) => left >= at);
    if (line) copy.textContent = line[1];
    label.textContent = left > 0 ? `Wait ${left}…` : "Press and hold to break my lock";
    if (left <= 0) {
      clearInterval(tick);
      copy.textContent = "If you really mean it, press and hold for 3 seconds.";
      btn.disabled = false;
    }
  }, 1000);

  const HOLD = 3000;
  let start = 0, raf = 0;
  const stop = () => { start = 0; cancelAnimationFrame(raf); fill.style.width = "0"; };
  const step = (t) => {
    if (!start) return;
    const p = Math.min((t - start) / HOLD, 1);
    fill.style.width = `${p * 100}%`;
    if (p >= 1) { start = 0; form.requestSubmit(); return; }
    raf = requestAnimationFrame(step);
  };
  const begin = (e) => {
    if (btn.disabled || start) return;
    e.preventDefault();
    start = performance.now();
    raf = requestAnimationFrame(step);
  };
  btn.addEventListener("click", (e) => e.preventDefault()); // a tap is not enough
  btn.addEventListener("pointerdown", begin);
  btn.addEventListener("pointerup", stop);
  btn.addEventListener("pointerleave", stop);
  btn.addEventListener("pointercancel", stop);
  btn.addEventListener("keydown", (e) => { if ((e.key === " " || e.key === "Enter") && !e.repeat) begin(e); });
  btn.addEventListener("keyup", (e) => { if (e.key === " " || e.key === "Enter") stop(); });
});
