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
