import * as THREE from "three";

const clamp = (value: number, min = 0, max = 1) => Math.min(max, Math.max(min, value));
const mix = (a: number, b: number, amount: number) => a + (b - a) * amount;
const smooth = (edge0: number, edge1: number, value: number) => {
  const amount = clamp((value - edge0) / (edge1 - edge0));
  return amount * amount * (3 - 2 * amount);
};

interface Envelope {
  seed: number;
  lane: number;
  depth: number;
  phase: number;
  speed: number;
  scale: number;
  tilt: number;
}

function envelopeTexture() {
  const canvas = document.createElement("canvas");
  canvas.width = 320;
  canvas.height = 200;
  const context = canvas.getContext("2d")!;
  context.fillStyle = "#f5eddf";
  context.strokeStyle = "rgba(79, 68, 66, .48)";
  context.lineWidth = 4;
  context.beginPath();
  context.roundRect(5, 5, 310, 190, 8);
  context.fill();
  context.stroke();
  context.beginPath();
  context.moveTo(6, 7);
  context.lineTo(160, 119);
  context.lineTo(314, 7);
  context.stroke();
  context.globalAlpha = .3;
  context.beginPath();
  context.moveTo(6, 194);
  context.lineTo(122, 94);
  context.moveTo(314, 194);
  context.lineTo(198, 94);
  context.stroke();

  const texture = new THREE.CanvasTexture(canvas);
  texture.colorSpace = THREE.SRGBColorSpace;
  texture.anisotropy = 4;
  return texture;
}

class OrderedMail {
  private film: HTMLElement;
  private canvas: HTMLCanvasElement;
  private renderer: THREE.WebGLRenderer;
  private scene = new THREE.Scene();
  private camera = new THREE.PerspectiveCamera(46, 1, .1, 100);
  private mesh: THREE.InstancedMesh;
  private envelopes: Envelope[] = [];
  private dummy = new THREE.Object3D();
  private progress = 0;
  private target = 0;
  private visible = true;
  private running = true;
  private raf = 0;
  private last = performance.now();
  private slowFrames = 0;
  private dprLimit = 1.5;

  constructor(film: HTMLElement, canvas: HTMLCanvasElement) {
    this.film = film;
    this.canvas = canvas;
    this.renderer = new THREE.WebGLRenderer({
      canvas,
      alpha: true,
      antialias: true,
      powerPreference: "high-performance",
    });
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.renderer.setClearColor(0x000000, 0);
    this.scene.fog = new THREE.FogExp2(0x655e69, .026);
    this.camera.position.set(0, .25, 11.8);

    const geometry = new THREE.PlaneGeometry(1.42, .89);
    const material = new THREE.MeshBasicMaterial({
      map: envelopeTexture(),
      transparent: true,
      opacity: .72,
      alphaTest: .02,
      side: THREE.DoubleSide,
      depthWrite: false,
      fog: true,
    });
    this.mesh = new THREE.InstancedMesh(geometry, material, 42);
    this.mesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
    this.mesh.frustumCulled = false;
    this.scene.add(this.mesh);

    for (let index = 0; index < 42; index++) {
      this.envelopes.push({
        seed: Math.random(),
        lane: Math.random() * 2 - 1,
        depth: Math.random(),
        phase: Math.random() * Math.PI * 2,
        speed: .22 + Math.random() * .42,
        scale: .34 + Math.random() * .62,
        tilt: Math.random() * 2 - 1,
      });
    }

    this.resize();
    this.readScroll();
    this.bind();
    this.film.classList.add("cinema-ready");
    this.raf = requestAnimationFrame((time) => this.frame(time));
  }

  private bind() {
    addEventListener("resize", () => this.resize(), { passive: true });
    addEventListener("scroll", () => this.readScroll(), { passive: true });
    document.addEventListener("visibilitychange", () => {
      this.running = !document.hidden;
      if (this.running && this.visible && !this.raf) this.raf = requestAnimationFrame((time) => this.frame(time));
    });
    new IntersectionObserver(([entry]) => {
      this.visible = entry.isIntersecting;
      if (this.visible && this.running && !this.raf) this.raf = requestAnimationFrame((time) => this.frame(time));
    }, { rootMargin: "100px" }).observe(this.film);
    this.canvas.addEventListener("webglcontextlost", () => {
      this.film.classList.remove("cinema-ready");
      this.running = false;
      if (this.raf) cancelAnimationFrame(this.raf);
      this.raf = 0;
    });
  }

  private readScroll() {
    const rect = this.film.getBoundingClientRect();
    const distance = Math.max(1, rect.height - innerHeight);
    this.target = clamp(-rect.top / distance);
    this.film.style.setProperty("--film-p", this.target.toFixed(4));
  }

  private resize() {
    const width = this.canvas.clientWidth || innerWidth;
    const height = this.canvas.clientHeight || innerHeight;
    this.renderer.setPixelRatio(Math.min(devicePixelRatio || 1, this.dprLimit));
    this.renderer.setSize(width, height, false);
    this.camera.aspect = width / Math.max(1, height);
    this.camera.updateProjectionMatrix();
  }

  private frame(time: number) {
    this.raf = 0;
    if (!this.running || !this.visible) return;

    const delta = Math.min(48, time - this.last);
    this.last = time;
    this.slowFrames = delta > 28 ? this.slowFrames + 1 : Math.max(0, this.slowFrames - 1);
    if (this.slowFrames > 45 && this.dprLimit > 1) {
      this.dprLimit = 1;
      this.slowFrames = 0;
      this.resize();
    }

    this.progress += (this.target - this.progress) * .07;
    this.update(time * .001);
    this.renderer.render(this.scene, this.camera);
    this.raf = requestAnimationFrame((next) => this.frame(next));
  }

  private update(time: number) {
    const order = smooth(.16, .76, this.progress);
    const settle = smooth(.62, .96, this.progress);
    const travelSpeed = mix(1, .08, settle);

    this.envelopes.forEach((envelope, index) => {
      const chaosX = ((time * envelope.speed + envelope.seed * 31) % 32) - 16;
      const chaosY = envelope.lane * 7 + Math.sin(time * 1.1 + envelope.phase + chaosX * .12) * 1.15;
      const chaosZ = 3 - envelope.depth * 30;

      const orderedX = ((time * envelope.speed * travelSpeed + envelope.seed * 31) % 32) - 16;
      const orderedY = ((index % 5) - 2) * .7 + Math.sin(time * .35 + envelope.phase) * .12 * (1 - settle);
      const orderedZ = -5 - envelope.depth * 24;
      const keep = index < 10 || index % 4 === 0;
      const visibility = keep ? 1 : mix(1, .001, settle);

      this.dummy.position.set(
        mix(chaosX, orderedX, order),
        mix(chaosY, orderedY, order),
        mix(chaosZ, orderedZ, order),
      );
      this.dummy.rotation.set(
        mix(Math.sin(time + envelope.phase) * .14, 0, order),
        mix(envelope.tilt * .9, -.05, order),
        mix(Math.sin(time * .8 + envelope.phase) * .34 + envelope.tilt * .3, 0, order),
      );
      this.dummy.scale.setScalar(envelope.scale * mix(1, .76, order) * visibility);
      this.dummy.updateMatrix();
      this.mesh.setMatrixAt(index, this.dummy.matrix);
    });
    this.mesh.instanceMatrix.needsUpdate = true;

    this.camera.position.x = mix(-.4, .75, order);
    this.camera.position.y = mix(.25, .5, settle);
    this.camera.position.z = mix(11.8, 10.4, order);
    this.camera.lookAt(mix(0, .6, order), 0, -7);
  }
}

const film = document.querySelector<HTMLElement>("[data-hero-film]");
const canvas = film?.querySelector<HTMLCanvasElement>("[data-cinema]");

if (film && canvas) {
  try {
    new OrderedMail(film, canvas);
  } catch (error) {
    film.classList.add("cinema-failed");
    console.warn("Cinematic layer unavailable; using the composed poster.", error);
  }
}
