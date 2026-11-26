# Services d'errand, une définition = un port : tour 3 (le dernier, sauf surprise)

> Laisse **Réponse** vide pour accepter la recommandation. `?` pour plus de détails.

## Q1 : Deux demandes locales, mêmes définitions, options de port différentes

`others`, `--listen`, le numéro imposé et `hold_end` ne sont pas dans l'identité : ils concernent les connexions. Mais le port, lui, est unique par définition. Si un client veut `--listen 0.0.0.0` et l'autre non, le port du second s'ouvrirait aussi.

Proposition : le **port** est celui de (définition + ces options). Deux demandes qui diffèrent ont deux ports, qui mènent au même serveur. Sans ces options, un seul port, comme prévu.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q2 : Arrêter et rendre

Le bail survit au serveur, donc deux verbes :

- `errand serve stop ollama` arrête le **serveur** ; le port reste, la prochaine connexion le redémarre ;
- `errand serve release ollama` rend le **bail** : le port se ferme (le numéro reste réservé 14 jours) et le serveur finit par `idle`. `--now` fait les deux.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q3 : Un serveur qui ne démarre pas

La connexion est fermée et la raison est dans le journal et la page. La bibliothèque, en voyant la connexion fermée sans réponse, demande l'état à errand et lève une erreur avec cette raison. Pour ne pas relancer en boucle : après un échec, les connexions suivantes sont refusées tout de suite pendant un délai qui double (2 s, puis jusqu'à 1 min).

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q4 : Ce qu'un demandeur restreint peut régler

Une clé restreinte ne donne qu'un hash. Alors `keep` et `idle` ne viennent pas d'elle : ils sont fixés dans l'autorisation, par `errand serve authorize --to key:boucles --idle 2m --keep 1d -- …` (mêmes arguments qu'une demande).

➡️ **Recommandé :** oui, l'autorisation fixe tout.

**Réponse :**

---

## Q5 : Les services déclarés dans les projets

Un `errand-*.py` de projet pouvait aussi déclarer des services. Avec `services.py`, ils disparaissent tous : `errand.services` et `errand.Service` ne sont plus lus nulle part, et le disent avec la marche à suivre.

➡️ **Recommandé :** oui, supprimés partout.

**Réponse :**

---

## Q6 : Le passage, pour lmo et Boucles

La clé de Boucles lance le lien stable de lmo, qui suit le build du Mac dès la prochaine commande vers lmo. Le jour où un build sans `services.py` arrive, `connect ollama` ne trouverait plus rien, et Boucles retomberait sur le Mac. Pour que ça ne casse pas :

1. `connect` accepte aussi une **étiquette** si une seule autorisation la porte : `connect ollama` continue de marcher, la clé ne change pas ;
2. au premier lancement du nouveau build sur lmo, errand **n'importe rien** : il dit que `services.py` n'est plus lu et donne la ligne `errand serve authorize …` équivalente, à taper par toi **avant** d'installer le build sur le Mac.

Je te donnerai cette ligne exacte ; tu la tapes sur lmo, puis tu installes.

➡️ **Recommandé :** oui, aucune importation automatique.

**Réponse :**

On peut arrêter Boucle le temps du dev. On redémarrera avec tout remis à jour sans ce soucier de retrocompatibilité

---

## Q7 : Les variables secrètes

Une définition peut contenir un jeton (`HF_TOKEN=…`). Les fichiers de baux et d'autorisations sont lisibles par toi seul (0600). La page et `status` montrent les **noms** de toutes les variables, et masquent la valeur de celles dont le nom contient `TOKEN`, `KEY`, `SECRET` ou `PASSWORD`.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q8 : L'ordre du travail

1. ADR 0040 réécrite, puis le bail local (port, durée, reprise par un agent, `--json`, arrêt) ; `services.py` supprimé à la fin de l'étape 2, pas avant ;
2. la définition, son hash, l'environnement de base, les sondes en données, les serveurs trouvés par définition ; `on`, la machine distante, la page ;
3. `errand-lib` Python ;
4. les autorisations, les pages, `there --only`, `connect` par hash ;
5. Boucles, `--listen`, `--always`, Node.

Chaque étape finit testée et la skill à jour ; la page montre les baux dès l'étape 1.

➡️ **Recommandé :** cet ordre.

**Réponse :**

---
