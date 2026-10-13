const LIMIT = 20;

class Widget {
  constructor(el) {
    this.el = el;
  }

  render() {
    this.el.textContent = `hello ${LIMIT}`;
  }
}

function plain(a) {
  return a + 1;
}

module.exports = { Widget, plain, LIMIT };
